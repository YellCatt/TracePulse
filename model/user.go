// Package model 定义了应用程序中使用的数据模型（数据库实体及请求/响应结构）。
package model

import "gorm.io/gorm"

// User 用户实体，对应数据库中的 users 表。
type User struct {
	gorm.Model
	Name string `json:"name" gorm:"not null"` // 用户姓名
	Age  int    `json:"age"`                  // 用户年龄
}

// CreateUserRequest 创建用户的请求参数。
type CreateUserRequest struct {
	Name string `json:"name" validate:"required"`              // 用户姓名（必填）
	Age  int    `json:"age" validate:"required,min=1,max=120"` // 用户年龄（必填，1-120）
}

// UpdateUserRequest 更新用户的请求参数，所有字段均为可选。
type UpdateUserRequest struct {
	Name string `json:"name"`                         // 用户姓名（可选）
	Age  int    `json:"age" validate:"min=1,max=120"` // 用户年龄（可选，1-120）
}
