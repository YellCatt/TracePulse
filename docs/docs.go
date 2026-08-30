// Package docs 包含 Swagger 文档的定义与注册逻辑，供 Swagger UI 使用。
package docs

import "github.com/swaggo/swag"

// docTemplate Swagger 文档的 JSON 模板定义。
const docTemplate = `{
  "swagger": "2.0",
  "info": {
    "description": "TracePulse - Go API Service",
    "title": "TracePulse",
    "contact": {},
    "version": "1.0"
  },
  "host": "localhost:8086",
  "basePath": "/",
  "paths": {
  },
  "definitions": {
  }
}`

// init 在包初始化时将 Swagger 规范注册到 swag 全局注册表。
func init() {
	swag.Register(swag.Name, &swag.Spec{
		Version:          "1.0",
		Host:             "localhost:8086",
		BasePath:         "/",
		Schemes:          []string{"http"},
		Title:            "TracePulse",
		Description:      "TracePulse - Go API Service",
		InfoInstanceName: "swagger",
		SwaggerTemplate:  docTemplate,
	})
}
