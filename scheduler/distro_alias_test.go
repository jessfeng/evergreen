package scheduler

import (
	"testing"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/db"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/model/distro"
	"github.com/evergreen-ci/evergreen/model/task"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
)

func TestDistroAliases(t *testing.T) {
	tasks := []task.Task{
		{
			Id:            "other",
			DistroId:      "one",
			Priority:      200,
			Version:       "foo",
			DistroAliases: []string{"two"},
		},
		{
			Id:            "one",
			DistroId:      "one",
			Priority:      2000,
			Version:       "foo",
			DistroAliases: []string{"two"},
		},
	}

	require.NoError(t, db.Clear(model.TaskQueuesCollection))
	require.NoError(t, db.Clear(model.TaskAliasQueuesCollection))

	require.NoError(t, db.Clear(model.VersionCollection))
	require.NoError(t, (&model.Version{Id: "foo"}).Insert())

	t.Run("VerifyPrimaryQueue", func(t *testing.T) {
		distroOne := &distro.Distro{
			Id: "one",
		}

		t.Run("Tunable", func(t *testing.T) {
			require.NoError(t, db.Clear(model.TaskQueuesCollection))

			distroOne.PlannerSettings.Version = evergreen.PlannerVersionTunable
			output, err := PrioritizeTasks(distroOne, tasks, TaskPlannerOptions{ID: "tunable-0"})
			require.NoError(t, err)
			require.Len(t, output, 2)
			require.Equal(t, "one", output[0].Id)
			require.Equal(t, "other", output[1].Id)

			ct, err := db.Count(model.TaskQueuesCollection, bson.M{})
			require.NoError(t, err)
			require.Equal(t, 1, ct)

			ct, err = db.Count(model.TaskAliasQueuesCollection, bson.M{})
			require.NoError(t, err)
			require.Equal(t, 0, ct)
		})
		t.Run("UseLegacy", func(t *testing.T) {
			require.NoError(t, db.Clear(model.TaskQueuesCollection))

			distroOne.PlannerSettings.Version = evergreen.PlannerVersionLegacy
			output, err := PrioritizeTasks(distroOne, tasks, TaskPlannerOptions{ID: "legacy-1"})
			require.NoError(t, err)
			require.Len(t, output, 2)
			require.Equal(t, "one", output[0].Id)
			require.Equal(t, "other", output[1].Id)

			ct, err := db.Count(model.TaskQueuesCollection, bson.M{})
			require.NoError(t, err)
			require.Equal(t, 1, ct)

			ct, err = db.Count(model.TaskAliasQueuesCollection, bson.M{})
			require.NoError(t, err)
			require.Equal(t, 0, ct)
		})
	})

	require.NoError(t, db.Clear(model.TaskQueuesCollection))
	require.NoError(t, db.Clear(model.TaskAliasQueuesCollection))

	t.Run("DistroAlias", func(t *testing.T) {
		distroTwo := &distro.Distro{
			Id: "two",
		}

		t.Run("Tunable", func(t *testing.T) {
			require.NoError(t, db.Clear(model.TaskAliasQueuesCollection))

			distroTwo.PlannerSettings.Version = evergreen.PlannerVersionTunable
			output, err := PrioritizeTasks(distroTwo, tasks, TaskPlannerOptions{ID: "tunable-0", IsSecondaryQueue: true})
			require.NoError(t, err)
			require.Len(t, output, 2)
			require.Equal(t, "one", output[0].Id)
			require.Equal(t, "other", output[1].Id)

			ct, err := db.Count(model.TaskAliasQueuesCollection, bson.M{})
			require.NoError(t, err)
			require.Equal(t, 1, ct)

			ct, err = db.Count(model.TaskQueuesCollection, bson.M{})
			require.NoError(t, err)
			require.Equal(t, 0, ct)

		})
		t.Run("UseLegacy", func(t *testing.T) {
			require.NoError(t, db.Clear(model.TaskAliasQueuesCollection))

			distroTwo.PlannerSettings.Version = evergreen.PlannerVersionLegacy
			output, err := PrioritizeTasks(distroTwo, tasks, TaskPlannerOptions{ID: "legacy-0", IsSecondaryQueue: true})
			require.NoError(t, err)
			require.Len(t, output, 2)
			require.Equal(t, "one", output[0].Id)
			require.Equal(t, "other", output[1].Id)

			ct, err := db.Count(model.TaskAliasQueuesCollection, bson.M{})
			require.NoError(t, err)
			require.Equal(t, 1, ct)

			ct, err = db.Count(model.TaskQueuesCollection, bson.M{})
			require.NoError(t, err)
			require.Equal(t, 0, ct)
		})
	})
}
