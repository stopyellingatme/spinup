// Package migrate registers and runs upgrade codemods.
//
// Each codemod lives in a file named NNNN_slug.go and registers from init.
// Codemods edit code through internal/codemod. Plan must leave the project
// unchanged. Apply must be idempotent. The first real codemod is 0001_web_svmigrate.go.
package migrate
