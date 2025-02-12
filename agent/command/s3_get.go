package command

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/evergreen-ci/evergreen/agent/internal"
	"github.com/evergreen-ci/evergreen/agent/internal/client"
	agentutil "github.com/evergreen-ci/evergreen/agent/util"
	"github.com/evergreen-ci/evergreen/util"
	"github.com/evergreen-ci/pail"
	"github.com/evergreen-ci/utility"
	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
)

// A plugin command to fetch a resource from an s3 bucket and download it to
// the local machine.
type s3get struct {
	// AwsKey and AwsSecret are the user's credentials for
	// authenticating interactions with s3.
	AwsKey    string `mapstructure:"aws_key" plugin:"expand"`
	AwsSecret string `mapstructure:"aws_secret" plugin:"expand"`

	// RemoteFile is the filepath of the file to get, within its bucket
	RemoteFile string `mapstructure:"remote_file" plugin:"expand"`

	// Region is the s3 region where the bucket is located. It defaults to
	// "us-east-1".
	Region string `mapstructure:"region" plugin:"region"`

	// Bucket is the s3 bucket holding the desired file
	Bucket string `mapstructure:"bucket" plugin:"expand"`

	// BuildVariants stores a list of MCI build variants to run the command for.
	// If the list is empty, it runs for all build variants.
	BuildVariants []string `mapstructure:"build_variants" plugin:"expand"`

	// Only one of these two should be specified. local_file indicates that the
	// s3 resource should be downloaded as-is to the specified file, and
	// extract_to indicates that the remote resource is a .tgz file to be
	// downloaded to the specified directory.
	LocalFile string `mapstructure:"local_file" plugin:"expand"`
	ExtractTo string `mapstructure:"extract_to" plugin:"expand"`

	bucket pail.Bucket

	base
}

func s3GetFactory() Command   { return &s3get{} }
func (c *s3get) Name() string { return "s3.get" }

// s3get-specific implementation of ParseParams.
func (c *s3get) ParseParams(params map[string]interface{}) error {
	if err := mapstructure.Decode(params, c); err != nil {
		return errors.Wrapf(err, "error decoding %v params", c.Name())
	}

	// make sure the command params are valid
	if err := c.validateParams(); err != nil {
		return errors.Wrapf(err, "error validating %v params", c.Name())
	}

	return nil
}

// Validate that all necessary params are set, and that only one of
// local_file and extract_to is specified.
func (c *s3get) validateParams() error {
	if c.AwsKey == "" {
		return errors.New("aws_key cannot be blank")
	}
	if c.AwsSecret == "" {
		return errors.New("aws_secret cannot be blank")
	}
	if c.RemoteFile == "" {
		return errors.New("remote_file cannot be blank")
	}

	if c.Region == "" {
		c.Region = endpoints.UsEast1RegionID
	}

	// make sure the bucket is valid
	if err := validateS3BucketName(c.Bucket); err != nil {
		return errors.Wrapf(err, "%v is an invalid bucket name", c.Bucket)
	}

	// make sure local file and extract-to dir aren't both specified
	if c.LocalFile != "" && c.ExtractTo != "" {
		return errors.New("cannot specify both local_file and extract_to directory")
	}

	// make sure one is specified
	if c.LocalFile == "" && c.ExtractTo == "" {
		return errors.New("must specify either local_file or extract_to")
	}

	return nil
}

func (c *s3get) shouldRunForVariant(buildVariantName string) bool {
	//No buildvariant filter, so run always
	if len(c.BuildVariants) == 0 {
		return true
	}

	//Only run if the buildvariant specified appears in our list.
	return utility.StringSliceContains(c.BuildVariants, buildVariantName)
}

// Apply the expansions from the relevant task config (including restricted expansions)
// to all appropriate fields of the s3get.
func (c *s3get) expandParams(conf *internal.TaskConfig) error {
	return util.ExpandValues(c, conf.GetExpansionsWithRestricted())
}

// Implementation of Execute.  Expands the parameters, and then fetches the
// resource from s3.
func (c *s3get) Execute(ctx context.Context,
	comm client.Communicator, logger client.LoggerProducer, conf *internal.TaskConfig) error {

	// expand necessary params
	if err := c.expandParams(conf); err != nil {
		return err
	}

	// validate the params
	if err := c.validateParams(); err != nil {
		return errors.Wrap(err, "expanded params are not valid")
	}

	// create pail bucket
	httpClient := utility.GetHTTPClient()
	httpClient.Timeout = s3HTTPClientTimeout
	defer utility.PutHTTPClient(httpClient)
	err := c.createPailBucket(httpClient)
	if err != nil {
		return errors.Wrap(err, "problem connecting to s3")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.bucket.Check(ctx); err != nil {
		return errors.Wrap(err, "invalid pail bucket")
	}

	if !c.shouldRunForVariant(conf.BuildVariant.Name) {
		logger.Task().Infof("Skipping S3 get of remote file %v for variant %v",
			c.RemoteFile, conf.BuildVariant.Name)
		return nil
	}

	// if the local file or extract_to is a relative path, join it to the
	// working dir
	if c.LocalFile != "" {
		if !filepath.IsAbs(c.LocalFile) {
			c.LocalFile = filepath.Join(conf.WorkDir, c.LocalFile)
		}

		if err := createEnclosingDirectoryIfNeeded(c.LocalFile); err != nil {
			return errors.Wrap(err, "unable to create local_file directory")
		}
	}

	if c.ExtractTo != "" {
		if !filepath.IsAbs(c.ExtractTo) {
			c.ExtractTo = filepath.Join(conf.WorkDir, c.ExtractTo)
		}

		if err := createEnclosingDirectoryIfNeeded(c.ExtractTo); err != nil {
			return errors.Wrap(err, "unable to create extract_to directory")
		}
	}

	errChan := make(chan error)
	go func() {
		errChan <- errors.WithStack(c.getWithRetry(ctx, logger))
	}()

	select {
	case err := <-errChan:
		return errors.WithStack(err)
	case <-ctx.Done():
		logger.Execution().Info("Received signal to terminate execution of S3 Get Command")
		return nil
	}

}

// Wrapper around the Get() function to retry it
func (c *s3get) getWithRetry(ctx context.Context, logger client.LoggerProducer) error {
	backoffCounter := getS3OpBackoff()
	timer := time.NewTimer(0)
	defer timer.Stop()

	for i := 1; i <= maxS3OpAttempts; i++ {
		logger.Task().Infof("fetching %s from s3 bucket %s (attempt %d of %d)",
			c.RemoteFile, c.Bucket, i, maxS3OpAttempts)

		select {
		case <-ctx.Done():
			return errors.New("s3 get operation aborted")
		case <-timer.C:
			err := errors.WithStack(c.get(ctx))
			if err == nil {
				return nil
			}

			logger.Execution().Errorf("problem getting %s from s3 bucket, retrying. [%v]",
				c.RemoteFile, err)
			timer.Reset(backoffCounter.Duration())
		}
	}

	return errors.Errorf("S3 get failed after %d attempts", maxS3OpAttempts)
}

// Fetch the specified resource from s3.
func (c *s3get) get(ctx context.Context) error {
	// either untar the remote, or just write to a file
	if c.LocalFile != "" {
		// remove the file, if it exists
		if utility.FileExists(c.LocalFile) {
			if err := os.RemoveAll(c.LocalFile); err != nil {
				return errors.Wrapf(err, "error clearing local file %v", c.LocalFile)
			}
		}

		// download to local file
		return errors.Wrapf(c.bucket.Download(ctx, c.RemoteFile, c.LocalFile),
			"error downloading %s to %s", c.RemoteFile, c.LocalFile)

	}

	reader, err := c.bucket.Reader(ctx, c.RemoteFile)
	if err != nil {
		return errors.WithStack(err)
	}
	if err := agentutil.ExtractTarball(ctx, reader, c.ExtractTo, []string{}); err != nil {
		return errors.Wrapf(err, "problem extracting %s from archive", c.RemoteFile)
	}

	return nil
}

func (c *s3get) createPailBucket(httpClient *http.Client) error {
	opts := pail.S3Options{
		Credentials: pail.CreateAWSCredentials(c.AwsKey, c.AwsSecret, ""),
		Region:      c.Region,
		Name:        c.Bucket,
	}
	bucket, err := pail.NewS3BucketWithHTTPClient(httpClient, opts)
	c.bucket = bucket
	return err
}
