module github.com/bytedance/sonic/fuzz

go 1.20

require (
	github.com/bytedance/sonic v1.11.5-alpha3
	github.com/davecgh/go-spew v1.1.1
	github.com/stretchr/testify v1.10.0
)

require (
	github.com/pmezard/go-difflib v1.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/bytedance/sonic => ../.
