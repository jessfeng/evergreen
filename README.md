# Evergreen
Evergreen is a distributed continuous integration system built by MongoDB.
It dynamically allocates hosts to run tasks in parallel across many machines.

See [the wiki](https://github.com/evergreen-ci/evergreen/wiki) for
user-facing documentation.

See [the API docs](https://pkg.go.dev/github.com/evergreen-ci/evergreen) for developer
documentation. For an overview of the architecture, see the list of directories
and their descriptions at the bottom of that page.

# Features

#### Elastic Host Allocation
Use only the computing resources you need.

#### Clean UI
Easily navigate the state of your tests, logs, and commit history.

#### Multiplatform Support
Run jobs on any platform Go can cross-compile to.

#### Spawn Hosts
Spin up a copy of any machine in your test infrastructure for debugging.

#### Patch Builds
See test results for your code changes before committing.

#### Stepback on Failure
Automatically run past commits to pinpoint the origin of a test failure.

## Go Requirements
* [Install Go 1.16 or later](https://golang.org/dl/).
* Set GO111MODULE="off". Make sure GOPATH and GOROOT are set.

## Building the Binaries

Setup:

* ensure that your `GOPATH` environment variable is set.
* check out a copy of the repo into your gopath. You can use: `go get
  github.com/evergreen-ci/evergreen`. If you have an existing checkout
  of the evergreen repository that is not in
  `$GOPATH/src/github.com/evergreen-ci/` move or create a symlink.

Possible Targets:

* run `make build` to compile a binary for your local
  system.
* run `make dist` to compile binaries for all supported systems
  and create a *dist* tarball with all artifacts.
