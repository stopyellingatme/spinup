// Package upgrade plans and coordinates template upgrades for generated projects.
// BuildPlan reads files without changing them. NextLock records template content
// independently of merged content so later upgrades preserve user edits.
package upgrade
