module github.com/bytedance/sonic/generic_test

go 1.20

require (
	github.com/bytedance/sonic v1.11.5-alpha3
	github.com/go-json-experiment/json v0.0.0-20220603215908-554802c1e539
	github.com/goccy/go-json v0.9.11
	github.com/json-iterator/go v1.1.12
)

require (
	github.com/modern-go/concurrent v0.0.0-20180228061459-e0a39a4cb421 // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
)

replace github.com/bytedance/sonic => ../.
