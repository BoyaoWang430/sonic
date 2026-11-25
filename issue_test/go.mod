module github.com/bytedance/sonic/issue_test

go 1.20

replace github.com/bytedance/sonic => ../.

require (
	github.com/bytedance/sonic v0.0.0-00010101000000-000000000000
	github.com/davecgh/go-spew v1.1.1
	github.com/stretchr/testify v1.10.0
)

require (
	github.com/pmezard/go-difflib v1.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
