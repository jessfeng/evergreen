package route

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/evergreen-ci/evergreen/model/event"
	"github.com/evergreen-ci/evergreen/rest/data"
	restModel "github.com/evergreen-ci/evergreen/rest/model"
	"github.com/evergreen-ci/utility"
	"github.com/stretchr/testify/suite"
)

type ProjectEventsTestSuite struct {
	suite.Suite
	sc        *data.MockConnector
	data      data.MockProjectConnector
	route     projectEventsGet
	projectId string
	event     restModel.APIProjectEvent
}

func TestProjectEventsTestSuite(t *testing.T) {

	suite.Run(t, new(ProjectEventsTestSuite))
}

func getMockProjectSettings(projectId string) restModel.APIProjectSettings {
	return restModel.APIProjectSettings{
		ProjectRef: restModel.APIProjectRef{
			Owner:      utility.ToStringPtr("admin"),
			Enabled:    utility.TruePtr(),
			Private:    utility.TruePtr(),
			Identifier: utility.ToStringPtr(projectId),
			Admins:     []*string{},
		},
		GitHubWebhooksEnabled: true,
		Vars: restModel.APIProjectVars{
			Vars:        map[string]string{},
			PrivateVars: map[string]bool{},
		},
		Aliases: []restModel.APIProjectAlias{restModel.APIProjectAlias{
			Alias:   utility.ToStringPtr("alias1"),
			Variant: utility.ToStringPtr("ubuntu"),
			Task:    utility.ToStringPtr("subcommand"),
		},
		},
		Subscriptions: []restModel.APISubscription{restModel.APISubscription{
			ID:           utility.ToStringPtr("subscription1"),
			ResourceType: utility.ToStringPtr("project"),
			Owner:        utility.ToStringPtr("admin"),
			Subscriber: restModel.APISubscriber{
				Type:   utility.ToStringPtr(event.GithubPullRequestSubscriberType),
				Target: restModel.APIGithubPRSubscriber{},
			},
		},
		},
	}
}

func (s *ProjectEventsTestSuite) SetupSuite() {
	s.projectId = "mci2"
	beforeSettings := getMockProjectSettings(s.projectId)

	afterSettings := getMockProjectSettings(s.projectId)
	afterSettings.ProjectRef.Enabled = utility.FalsePtr()

	s.event = restModel.APIProjectEvent{
		Timestamp: restModel.ToTimePtr(time.Now()),
		User:      utility.ToStringPtr("me"),
		Before:    beforeSettings,
		After:     afterSettings,
	}

	s.data = data.MockProjectConnector{
		CachedEvents: []restModel.APIProjectEvent{s.event},
	}

	s.sc = &data.MockConnector{
		URL:                  "https://evergreen.example.net",
		MockProjectConnector: s.data,
	}
}

func (s *ProjectEventsTestSuite) TestGetProjectEvents() {
	s.route.Id = s.projectId
	s.route.Limit = 100
	s.route.Timestamp = time.Now().Add(time.Second * 10)
	s.route.sc = s.sc

	resp := s.route.Run(context.Background())
	s.NotNil(resp)
	s.Equal(http.StatusOK, resp.Status())

	responseData, ok := resp.Data().([]interface{})
	s.Require().True(ok)
	s.Equal(&s.event, responseData[0])
}
