// Package router 定义了 HTTP 路由注册逻辑，将 URL 路径映射到对应的 Controller 方法。
package router

import (
	"net/http"
	"time"

	"github.com/example/tracepulse/controller"
	"github.com/example/tracepulse/logger"
	httpSwagger "github.com/swaggo/http-swagger/v2"
	"go.uber.org/zap"
)

// NewRouter 创建并配置 HTTP 请求路由器。
//
// Go 1.22 的 http.ServeMux 支持方法 + 路径参数，且「更具体的字面量路径」优先于
// 通配段，因此 /api/traces/stats 会先于 /api/traces/{trace_id} 命中。
func NewRouter(
	userController *controller.UserController,
	statusController *controller.StatusController,
	traceController *controller.TraceController,
) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","message":"Service is running"}`))
	})

	mux.HandleFunc("GET /status", statusController.GetStatus)

	// ---------------- 链路：JSON API ----------------
	mux.HandleFunc("POST /api/traces/report", traceController.ReportEvents)
	mux.HandleFunc("GET /api/traces", traceController.ListTracesJSON)
	mux.HandleFunc("GET /api/traces/stats", traceController.StatsJSON)
	mux.HandleFunc("GET /api/traces/{trace_id}", traceController.GetTraceJSON)

	// ---------------- 链路：内置页面 ----------------
	mux.HandleFunc("GET /traces", traceController.SearchPage)
	mux.HandleFunc("GET /trace/{trace_id}", traceController.TraceDetailPage)
	// "/{$}" 只精确匹配根路径。如果写成 "GET /" 就成了全方法通配，
	// 会与下面的 "/swagger/" 前缀路由冲突并直接 panic。
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/traces", http.StatusFound)
	})

	// ---------------- 用户 ----------------
	mux.HandleFunc("POST /api/users", userController.CreateUser)
	mux.HandleFunc("GET /api/users", userController.GetAllUsers)
	mux.HandleFunc("GET /api/users/{id}", userController.GetUserByID)
	mux.HandleFunc("PUT /api/users/{id}", userController.UpdateUser)
	mux.HandleFunc("DELETE /api/users/{id}", userController.DeleteUser)

	// ---------------- 文档 ----------------
	mux.Handle("/swagger/", httpSwagger.Handler(
		httpSwagger.URL("/swagger/doc.json"),
	))

	mux.HandleFunc("GET /swagger/doc.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(swaggerDoc))
	})

	logger.Debug("http routes registered")
	return withRequestLog(mux)
}

// withRequestLog 包装 mux，记录每个 HTTP 请求的方法、路径与耗时（Debug 级别）。
func withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.Debug("http request handled",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", rec.status),
			zap.String("remote_addr", r.RemoteAddr),
			zap.Duration("elapsed", time.Since(start)),
		)
	})
}

// statusRecorder 记录响应状态码，供请求日志输出。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// swaggerDoc 内嵌的 Swagger 2.0 文档定义，用于 Swagger UI 展示 API 文档。
const swaggerDoc = `{
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
    "/api/traces/report": {
      "post": {
        "description": "上报链路事件，非阻塞，队列满时丢弃事件",
        "consumes": ["application/json"],
        "produces": ["application/json"],
        "tags": ["traces"],
        "summary": "上报链路事件",
        "parameters": [
          {
            "description": "事件数组，也支持 {\"events\":[...]} 形式",
            "name": "body",
            "in": "body",
            "required": true,
            "schema": { "$ref": "#/definitions/model.ReportRequest" }
          }
        ],
        "responses": {
          "200": { "description": "Success" },
          "400": { "description": "Bad Request" },
          "413": { "description": "Payload Too Large" }
        }
      }
    },
    "/api/traces": {
      "get": {
        "description": "多条件过滤 + 分页查询链路列表",
        "produces": ["application/json"],
        "tags": ["traces"],
        "summary": "检索链路列表",
        "parameters": [
          { "type": "string", "name": "trace_id", "in": "query", "description": "trace_id 精确匹配" },
          { "type": "string", "name": "service", "in": "query", "description": "服务名" },
          { "type": "string", "name": "status", "in": "query", "description": "ok/error/warn/timeout" },
          { "type": "string", "name": "level", "in": "query", "description": "链路中出现过的级别" },
          { "type": "string", "name": "module", "in": "query", "description": "链路中出现过的模块" },
          { "type": "string", "name": "keyword", "in": "query", "description": "模糊搜索关键词" },
          { "type": "boolean", "name": "has_error", "in": "query", "description": "是否含错误" },
          { "type": "integer", "name": "min_duration_ms", "in": "query", "description": "慢调用阈值（毫秒）" },
          { "type": "string", "name": "start_time", "in": "query", "description": "开始时间，支持 RFC3339 / 2026-01-02 15:04:05 / 1h" },
          { "type": "string", "name": "end_time", "in": "query", "description": "结束时间" },
          { "type": "integer", "name": "page", "in": "query", "default": 1 },
          { "type": "integer", "name": "page_size", "in": "query", "default": 20 }
        ],
        "responses": {
          "200": {
            "description": "Success",
            "schema": { "$ref": "#/definitions/model.TraceListResult" }
          }
        }
      }
    },
    "/api/traces/{trace_id}": {
      "get": {
        "description": "按 trace_id 精确查询完整链路",
        "produces": ["application/json"],
        "tags": ["traces"],
        "summary": "查询链路详情",
        "parameters": [
          { "type": "string", "name": "trace_id", "in": "path", "required": true }
        ],
        "responses": {
          "200": {
            "description": "Success",
            "schema": { "$ref": "#/definitions/model.TraceDetail" }
          },
          "404": { "description": "Not Found" }
        }
      }
    },
    "/api/traces/stats": {
      "get": {
        "description": "队列水位与运行时指标",
        "produces": ["application/json"],
        "tags": ["traces"],
        "summary": "运行时指标",
        "responses": { "200": { "description": "Success" } }
      }
    },
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
    },
    "model.ReportRequest": {
      "type": "object",
      "properties": {
        "events": {
          "type": "array",
          "items": { "$ref": "#/definitions/model.TraceEvent" }
        }
      }
    },
    "model.TraceEvent": {
      "type": "object",
      "properties": {
        "trace_id": { "type": "string" },
        "span_id": { "type": "string" },
        "parent_span_id": { "type": "string" },
        "timestamp": { "type": "string", "format": "date-time" },
        "level": { "type": "string" },
        "module": { "type": "string" },
        "event": { "type": "string" },
        "message": { "type": "string" },
        "params": { "type": "string", "description": "JSON 对象字符串" },
        "error_message": { "type": "string" }
      }
    },
    "model.Trace": {
      "type": "object",
      "properties": {
        "id": { "type": "integer" },
        "trace_id": { "type": "string" },
        "service_name": { "type": "string" },
        "status": { "type": "string" },
        "start_time": { "type": "string", "format": "date-time" },
        "end_time": { "type": "string", "format": "date-time" },
        "duration_ms": { "type": "integer" },
        "has_error": { "type": "boolean" },
        "error_message": { "type": "string" },
        "event_count": { "type": "integer" }
      }
    },
    "model.TraceListResult": {
      "type": "object",
      "properties": {
        "total": { "type": "integer" },
        "traces": {
          "type": "array",
          "items": { "$ref": "#/definitions/model.Trace" }
        },
        "page": { "type": "integer" },
        "page_size": { "type": "integer" },
        "total_pages": { "type": "integer" }
      }
    },
    "model.TraceDetail": {
      "type": "object",
      "properties": {
        "trace": { "$ref": "#/definitions/model.Trace" },
        "events": {
          "type": "array",
          "items": { "$ref": "#/definitions/model.TraceEvent" }
        }
      }
    }
  }
}`
