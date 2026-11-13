// Package issues is an example app that gives Dive two tools over a small
// issue tracker: create_issue files an issue as the run's user, in the
// run's tenant, and get_issue reads one back.
//
// [DiveTools] defines the tools with the tools package. The divetools
// command writes their files to .dive/tools, the plans subpackage holds
// typed wrappers for the file_issue plan in .dive/plans, and the issues
// command serves the tools and starts the plan. Run go generate in this
// directory after changing a tool or a plan.
package issues
