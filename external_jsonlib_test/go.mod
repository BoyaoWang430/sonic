module github.com/bytedance/sonic/external_jsonlib_test

go 1.20

require (
	github.com/bitly/go-simplejson v0.5.1
	github.com/buger/jsonparser v1.1.1
	github.com/bytedance/sonic v1.11.5-alpha3
	github.com/goccy/go-json v0.9.11
	github.com/json-iterator/go v1.1.12
	github.com/stretchr/testify v1.10.0
	github.com/tidwall/gjson v1.14.3
	github.com/tidwall/sjson v1.2.5
	github.com/valyala/fastjson v1.6.4
	github.com/vmihailenco/msgpack/v5 v5.4.1
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/modern-go/concurrent v0.0.0-20180228061459-e0a39a4cb421 // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/bytedance/sonic => ../.
