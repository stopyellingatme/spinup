// Package render creates deterministic file plans from packs, answers, and versions.
// It never writes files. Render completes answers through ValidateAnswers.
// Input packs and files retain their declared order in the returned plan.
// Text uses manifest.Normalize. Go outputs also use go/format.
// Binary content remains unchanged under manifest.Normalize.
//
// Template actions use [[ and ]] delimiters and missingkey=error.
// Answers form the template data, with reserved Stack and TemplateVersion keys.
// Stack contains the supplied *stackversions.Stack. TemplateVersion is a string.
// Answer keys cannot use these reserved names.
//
// Functions lower and upper convert Unicode case.
// Functions snake and kebab split words and join them with underscores or hyphens.
// Functions camel and pascal split words and join them in lower or upper camel case.
// Word boundaries include punctuation, camel case, and uppercase acronym boundaries.
// Function quote returns a Go string literal through strconv.Quote.
// Function join takes a separator and []string, in that order.
// Function indent takes a nonnegative width and text, in that order.
// It prefixes every line with that many spaces. Empty text remains empty.
// Function default takes a fallback and value, in that order.
// It returns the fallback for nil, false, zero numbers, or empty strings and containers.
// Function has takes a string and []string and reports membership.
//
// Function image takes an image key and returns Stack.Image(key).Ref().
// Function npm takes a package name and returns its pinned version.
// Function gomod takes a module path and returns its pinned version.
// Function uses takes an owner/repo name and returns owner/repo@sha # tag.
// Missing stack versions and unknown lookup keys fail the render.
// These functions use no clock, randomness, subprocess, or external file access.
package render
