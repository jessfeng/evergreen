// +build !race

package cloud

import (
	"context"
	"testing"
	"time"

	"github.com/evergreen-ci/birch"
	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/model/distro"
	"github.com/evergreen-ci/evergreen/model/host"
	"github.com/evergreen-ci/evergreen/testutil"
	"github.com/stretchr/testify/suite"
)

type CostUnitSuite struct {
	suite.Suite
	rates []spotRate
}

func TestCostUnitSuite(t *testing.T) {
	suite.Run(t, new(CostUnitSuite))
}

// mins returns a time X minutes after UNIX epoch
func mins(x int64) time.Time {
	return time.Unix(60*x, 0)
}

func (s *CostUnitSuite) SetupTest() {
	s.rates = []spotRate{
		{Time: mins(0), Price: 1.0},
		{Time: mins(60), Price: .5},
		{Time: mins(2 * 60), Price: 1.0},
		{Time: mins(3 * 60), Price: 2.0},
		{Time: mins(4 * 60), Price: 1.0},
	}
}

func (s *CostUnitSuite) TestOnDemandPriceAPITranslation() {
	s.Equal("Linux", osBillingName(osLinux))
	s.Equal(string(osSUSE), osBillingName(osSUSE))
	s.Equal(string(osWindows), osBillingName(osWindows))
	r, err := regionFullname("us-east-1")
	s.NoError(err)
	s.Equal("US East (N. Virginia)", r)
	r, err = regionFullname("us-west-1")
	s.NoError(err)
	s.Equal("US West (N. California)", r)
	r, err = regionFullname("us-west-2")
	s.NoError(err)
	s.Equal("US West (Oregon)", r)
	_, err = regionFullname("amazing")
	s.Error(err)
}

func (s *CostUnitSuite) TestTimeTilNextPayment() {
	hourlyHost := host.Host{
		Id: "hourlyHost",
		Distro: distro.Distro{
			Arch: "windows_amd64",
		},
		CreationTime: time.Date(2017, 1, 1, 0, 30, 0, 0, time.Local),
		StartTime:    time.Date(2017, 1, 1, 1, 0, 0, 0, time.Local),
	}
	secondlyHost := host.Host{
		Id: "secondlyHost",
		Distro: distro.Distro{
			Arch: "linux_amd64",
		},
		CreationTime: time.Date(2017, 1, 1, 0, 0, 0, 0, time.Local),
		StartTime:    time.Date(2017, 1, 1, 0, 30, 0, 0, time.Local),
	}
	hourlyHostNoStartTime := host.Host{
		Id: "hourlyHostNoStartTime",
		Distro: distro.Distro{
			Arch: "windows_amd64",
		},
		CreationTime: time.Date(2017, 1, 1, 0, 0, 0, 0, time.Local),
	}
	now := time.Now()
	timeTilNextHour := int(time.Hour) - (now.Minute()*int(time.Minute) + now.Second()*int(time.Second) + now.Nanosecond()*int(time.Nanosecond))

	timeNextPayment := timeTilNextEC2Payment(&hourlyHost)
	s.InDelta(timeTilNextHour, timeNextPayment.Nanoseconds(), float64(1*time.Millisecond))

	timeNextPayment = timeTilNextEC2Payment(&secondlyHost)
	s.InDelta(1*time.Second, timeNextPayment.Nanoseconds(), float64(1*time.Millisecond))

	timeNextPayment = timeTilNextEC2Payment(&hourlyHostNoStartTime)
	s.InDelta(timeTilNextHour, timeNextPayment.Nanoseconds(), float64(1*time.Millisecond))
}

type CostIntegrationSuite struct {
	suite.Suite
	m      *ec2Manager
	client AWSClient
	h      *host.Host
	ctx    context.Context
	cancel context.CancelFunc
}

func TestCostIntegrationSuite(t *testing.T) {
	suite.Run(t, new(CostIntegrationSuite))
}

func (s *CostIntegrationSuite) SetupSuite() {
	s.ctx, s.cancel = context.WithCancel(context.Background())
	env := testutil.NewEnvironment(s.ctx, s.T())
	settings := env.Settings()
	testutil.ConfigureIntegrationTest(s.T(), settings, "CostIntegrationSuite")

	s.m = &ec2Manager{env: env, EC2ManagerOptions: &EC2ManagerOptions{client: &awsClientImpl{}}}
	s.NoError(s.m.Configure(s.ctx, settings))
	s.NoError(s.m.client.Create(s.m.credentials, evergreen.DefaultEC2Region))
	s.client = s.m.client
}

func (s *CostIntegrationSuite) TearDownSuite() {
	s.cancel()
}

func (s *CostIntegrationSuite) SetupTest() {
	pkgCachingPriceFetcher.ec2Prices = nil
	s.h = &host.Host{
		Id: "h1",
		Distro: distro.Distro{
			ProviderSettingsList: []*birch.Document{birch.NewDocument(
				birch.EC.String("ami", "ami"),
				birch.EC.String("key_name", "key"),
				birch.EC.String("instance_type", "instance"),
				birch.EC.String("aws_access_key_id", "key_id"),
				birch.EC.Double("bid_price", 0.001),
				birch.EC.SliceString("security_group_ids", []string{"abcdef"}),
			)},
			Provider: evergreen.ProviderNameEc2OnDemand,
		},
	}
}

func (s *CostIntegrationSuite) TestFetchOnDemandPricingCached() {
	pkgCachingPriceFetcher.ec2Prices = map[odInfo]float64{
		odInfo{os: "Linux", instance: "c3.4xlarge", region: "US East (N. Virginia)"}:   .1,
		odInfo{os: "Windows", instance: "c3.4xlarge", region: "US East (N. Virginia)"}: .2,
		odInfo{os: "Linux", instance: "c3.xlarge", region: "US East (N. Virginia)"}:    .3,
		odInfo{os: "Windows", instance: "c3.xlarge", region: "US East (N. Virginia)"}:  .4,
		odInfo{os: "Linux", instance: "m5.4xlarge", region: "US East (N. Virginia)"}:   .5,
		odInfo{os: "Windows", instance: "m5.4xlarge", region: "US East (N. Virginia)"}: .6,
		odInfo{os: "Linux", instance: "m5.xlarge", region: "US East (N. Virginia)"}:    .7,
		odInfo{os: "Windows", instance: "m5.xlarge", region: "US East (N. Virginia)"}:  .8,
	}

	price, err := pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osLinux, "c3.4xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(.1, price)

	price, err = pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osWindows, "c3.4xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(.2, price)

	price, err = pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osLinux, "c3.xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(.3, price)

	price, err = pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osWindows, "c3.xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(.4, price)

	price, err = pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osLinux, "m5.4xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(.5, price)

	price, err = pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osWindows, "m5.4xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(.6, price)

	price, err = pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osLinux, "m5.xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(.7, price)

	price, err = pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osWindows, "m5.xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(.8, price)
}

func (s *CostIntegrationSuite) TestFetchOnDemandPricingUncached() {
	price, err := pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osLinux, "c3.4xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(.84, price)

	price, err = pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osWindows, "c3.4xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(1.504, price)

	price, err = pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osLinux, "c3.xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(.21, price)

	price, err = pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osWindows, "c3.xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(.376, price)

	price, err = pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osLinux, "m5.4xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(.768, price)

	price, err = pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osWindows, "m5.4xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(1.504, price)

	price, err = pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osLinux, "m5.xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(.192, price)

	price, err = pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, osWindows, "m5.xlarge", "us-east-1")
	s.NoError(err)
	s.Equal(.376, price)

	s.Equal(.84, pkgCachingPriceFetcher.ec2Prices[odInfo{os: "Linux", instance: "c3.4xlarge", region: "US East (N. Virginia)"}])
	s.Equal(1.504, pkgCachingPriceFetcher.ec2Prices[odInfo{os: "Windows", instance: "c3.4xlarge", region: "US East (N. Virginia)"}])
	s.Equal(.21, pkgCachingPriceFetcher.ec2Prices[odInfo{os: "Linux", instance: "c3.xlarge", region: "US East (N. Virginia)"}])
	s.Equal(.376, pkgCachingPriceFetcher.ec2Prices[odInfo{os: "Windows", instance: "c3.xlarge", region: "US East (N. Virginia)"}])
	s.Equal(.768, pkgCachingPriceFetcher.ec2Prices[odInfo{os: "Linux", instance: "m5.4xlarge", region: "US East (N. Virginia)"}])
	s.Equal(1.504, pkgCachingPriceFetcher.ec2Prices[odInfo{os: "Windows", instance: "m5.4xlarge", region: "US East (N. Virginia)"}])
	s.Equal(.192, pkgCachingPriceFetcher.ec2Prices[odInfo{os: "Linux", instance: "m5.xlarge", region: "US East (N. Virginia)"}])
	s.Equal(.376, pkgCachingPriceFetcher.ec2Prices[odInfo{os: "Windows", instance: "m5.xlarge", region: "US East (N. Virginia)"}])
}

func (s *CostIntegrationSuite) TestGetProviderStatic() {
	settings := &EC2ProviderSettings{}
	settings.InstanceType = "m4.large"
	settings.IsVpc = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.m.provider = onDemandProvider
	provider, err := s.m.getProvider(ctx, s.h, settings)
	s.NoError(err)
	s.Equal(onDemandProvider, provider)

	s.m.provider = spotProvider
	provider, err = s.m.getProvider(ctx, s.h, settings)
	s.NoError(err)
	s.Equal(spotProvider, provider)

	s.m.provider = 5
	_, err = s.m.getProvider(ctx, s.h, settings)
	s.Error(err)

	s.m.provider = -5
	_, err = s.m.getProvider(ctx, s.h, settings)
	s.Error(err)
}

func (s *CostIntegrationSuite) TestGetProviderAuto() {
	s.h.Distro.Arch = "linux"
	settings := &EC2ProviderSettings{}
	s.m.provider = autoProvider

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m4LargeOnDemand, err := pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, getOsName(s.h), "m4.large", evergreen.DefaultEC2Region)
	s.InDelta(.1, m4LargeOnDemand, .05)
	s.NoError(err)

	t2MicroOnDemand, err := pkgCachingPriceFetcher.getEC2OnDemandCost(context.Background(), s.m.client, getOsName(s.h), "t2.micro", evergreen.DefaultEC2Region)
	s.InDelta(.0116, t2MicroOnDemand, .01)
	s.NoError(err)

	settings.InstanceType = "m4.large"
	settings.IsVpc = true
	m4LargeSpot, az, err := pkgCachingPriceFetcher.getLatestSpotCostForInstance(ctx, s.m.client, settings, getOsName(s.h), "")
	s.Contains(az, "us-east")
	s.True(m4LargeSpot > 0)
	s.NoError(err)

	settings.InstanceType = "t2.micro"
	settings.IsVpc = true
	t2MicroSpot, az, err := pkgCachingPriceFetcher.getLatestSpotCostForInstance(ctx, s.m.client, settings, getOsName(s.h), "")
	s.Contains(az, "us-east")
	s.True(t2MicroSpot > 0)
	s.NoError(err)

	settings.InstanceType = "m4.large"
	settings.IsVpc = true
	provider, err := s.m.getProvider(ctx, s.h, settings)
	s.NoError(err)
	if m4LargeSpot < m4LargeOnDemand {
		s.Equal(spotProvider, provider)
		s.Equal(evergreen.ProviderNameEc2Spot, s.h.Distro.Provider)
	} else {
		s.Equal(onDemandProvider, provider)
		s.Equal(evergreen.ProviderNameEc2OnDemand, s.h.Distro.Provider)
	}

	settings.InstanceType = "t2.micro"
	settings.IsVpc = true
	provider, err = s.m.getProvider(ctx, s.h, settings)
	s.NoError(err)
	if t2MicroSpot < t2MicroOnDemand {
		s.Equal(spotProvider, provider)
		s.Equal(evergreen.ProviderNameEc2Spot, s.h.Distro.Provider)
	} else {
		s.Equal(onDemandProvider, provider)
		s.Equal(evergreen.ProviderNameEc2OnDemand, s.h.Distro.Provider)
	}
}
