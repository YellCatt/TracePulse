// Package controller 定义了 HTTP 请求处理器（Controller 层），负责解析请求并调用 Service 层。
package controller

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/example/tracepulse/logger"
	"github.com/example/tracepulse/model"
	"github.com/example/tracepulse/service"
	"go.uber.org/zap"
)

// UserController 用户相关的 HTTP 请求处理器。
type UserController struct {
	service service.UserService // 用户业务逻辑实例
}

// NewUserController 创建一个新的用户控制器实例。
func NewUserController(service service.UserService) *UserController {
	return &UserController{service: service}
}

// CreateUser 处理 POST /api/users 请求，创建新用户。
func (c *UserController) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req model.CreateUserRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		logger.Warn("create user rejected: bad request", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	user, err := c.service.CreateUser(&req)
	if err != nil {
		logger.Error("create user failed", zap.String("name", req.Name), zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	logger.Debug("user created",
		zap.Uint("id", user.ID),
		zap.String("name", user.Name),
		zap.Int("age", user.Age),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(user)
}

// GetUserByID 处理 GET /api/users/{id} 请求，根据 ID 查询用户。
func (c *UserController) GetUserByID(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		logger.Warn("get user rejected: invalid id", zap.String("id", idStr))
		http.Error(w, "invalid user ID", http.StatusBadRequest)
		return
	}

	user, err := c.service.GetUserByID(uint(id))
	if err != nil {
		logger.Error("get user failed", zap.Uint64("id", id), zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if user == nil {
		logger.Debug("user not found", zap.Uint64("id", id))
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	logger.Debug("user fetched",
		zap.Uint64("id", id),
		zap.String("name", user.Name),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(user)
}

// GetAllUsers 处理 GET /api/users 请求，查询所有用户。
func (c *UserController) GetAllUsers(w http.ResponseWriter, r *http.Request) {
	users, err := c.service.GetAllUsers()
	if err != nil {
		logger.Error("get all users failed", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	logger.Debug("all users fetched", zap.Int("count", len(users)))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(users)
}

// UpdateUser 处理 PUT /api/users/{id} 请求，更新指定用户。
func (c *UserController) UpdateUser(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		logger.Warn("update user rejected: invalid id", zap.String("id", idStr))
		http.Error(w, "invalid user ID", http.StatusBadRequest)
		return
	}

	var req model.UpdateUserRequest
	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		logger.Warn("update user rejected: bad request", zap.Uint64("id", id), zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	user, err := c.service.UpdateUser(uint(id), &req)
	if err != nil {
		logger.Error("update user failed", zap.Uint64("id", id), zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if user == nil {
		logger.Debug("update user: not found", zap.Uint64("id", id))
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	logger.Debug("user updated",
		zap.Uint64("id", id),
		zap.String("name", user.Name),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(user)
}

// DeleteUser 处理 DELETE /api/users/{id} 请求，删除指定用户。
func (c *UserController) DeleteUser(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		logger.Warn("delete user rejected: invalid id", zap.String("id", idStr))
		http.Error(w, "invalid user ID", http.StatusBadRequest)
		return
	}

	err = c.service.DeleteUser(uint(id))
	if err != nil {
		logger.Error("delete user failed", zap.Uint64("id", id), zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	logger.Debug("user deleted", zap.Uint64("id", id))

	w.WriteHeader(http.StatusNoContent)
}
