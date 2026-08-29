// Package docs 包含 Swagger 文档的定义与注册逻辑，供 Swagger UI 使用。
package docs

import "github.com/swaggo/swag"

// docTemplate Swagger 文档的 JSON 模板定义。
const docTemplate = `{
  "swagger": "2.0",
  "info": {
    "description": "GAPI - Go API Service",
    "title": "GAPI",
    "contact": {},
    "version": "1.0"
  },
  "host": "localhost:8084",
  "basePath": "/",
  "paths": {
    "/api/users": {
      "get": {
        "description": "Get all users",
        "consumes": ["application/json"],
        "produces": ["application/json"],
        "tags": ["users"],
        "summary": "Get all users",
        "responses": {
          "200": {
            "description": "Success",
            "schema": {
              "type": "array",
              "items": { "$ref": "#/definitions/model.User" }
            }
          }
        }
      },
      "post": {
        "description": "Create a new user",
        "consumes": ["application/json"],
        "produces": ["application/json"],
        "tags": ["users"],
        "summary": "Create user",
        "parameters": [
          {
            "description": "User object",
            "name": "user",
            "in": "body",
            "required": true,
            "schema": { "$ref": "#/definitions/model.CreateUserRequest" }
          }
        ],
        "responses": {
          "201": {
            "description": "Created",
            "schema": { "$ref": "#/definitions/model.User" }
          },
          "400": { "description": "Bad Request" }
        }
      }
    },
    "/api/users/{id}": {
      "get": {
        "description": "Get user by ID",
        "consumes": ["application/json"],
        "produces": ["application/json"],
        "tags": ["users"],
        "summary": "Get user",
        "parameters": [
          {
            "type": "integer",
            "description": "User ID",
            "name": "id",
            "in": "path",
            "required": true
          }
        ],
        "responses": {
          "200": {
            "description": "Success",
            "schema": { "$ref": "#/definitions/model.User" }
          },
          "404": { "description": "Not Found" }
        }
      },
      "put": {
        "description": "Update user",
        "consumes": ["application/json"],
        "produces": ["application/json"],
        "tags": ["users"],
        "summary": "Update user",
        "parameters": [
          {
            "type": "integer",
            "description": "User ID",
            "name": "id",
            "in": "path",
            "required": true
          },
          {
            "description": "User object",
            "name": "user",
            "in": "body",
            "required": true,
            "schema": { "$ref": "#/definitions/model.UpdateUserRequest" }
          }
        ],
        "responses": {
          "200": {
            "description": "Success",
            "schema": { "$ref": "#/definitions/model.User" }
          },
          "404": { "description": "Not Found" }
        }
      },
      "delete": {
        "description": "Delete user",
        "consumes": ["application/json"],
        "produces": ["application/json"],
        "tags": ["users"],
        "summary": "Delete user",
        "parameters": [
          {
            "type": "integer",
            "description": "User ID",
            "name": "id",
            "in": "path",
            "required": true
          }
        ],
        "responses": {
          "204": { "description": "No Content" },
          "404": { "description": "Not Found" }
        }
      }
    }
  },
  "definitions": {
    "model.CreateUserRequest": {
      "type": "object",
      "properties": {
        "name": { "type": "string" },
        "age": { "type": "integer" }
      },
      "required": ["name", "age"]
    },
    "model.UpdateUserRequest": {
      "type": "object",
      "properties": {
        "name": { "type": "string" },
        "age": { "type": "integer" }
      }
    },
    "model.User": {
      "type": "object",
      "properties": {
        "ID": { "type": "integer" },
        "CreatedAt": { "type": "string", "format": "date-time" },
        "UpdatedAt": { "type": "string", "format": "date-time" },
        "DeletedAt": { "type": "string", "format": "date-time" },
        "name": { "type": "string" },
        "age": { "type": "integer" }
      }
    }
  }
}`

// init 在包初始化时将 Swagger 规范注册到 swag 全局注册表。
func init() {
	swag.Register(swag.Name, &swag.Spec{
		Version:          "1.0",
		Host:             "localhost:8084",
		BasePath:         "/",
		Schemes:          []string{"http"},
		Title:            "GAPI",
		Description:      "GAPI - Go API Service",
		InfoInstanceName: "swagger",
		SwaggerTemplate:  docTemplate,
	})
}
