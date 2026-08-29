// Package service 定义了业务逻辑层，协调 repository 与 controller 之间的交互。
package service

import (
	"github.com/example/gapi/model"
	"github.com/example/gapi/repository"
)

// UserService 用户业务逻辑接口。
type UserService interface {
	CreateUser(req *model.CreateUserRequest) (*model.User, error)
	GetUserByID(id uint) (*model.User, error)
	GetAllUsers() ([]model.User, error)
	UpdateUser(id uint, req *model.UpdateUserRequest) (*model.User, error)
	DeleteUser(id uint) error
}

// userService UserService 的默认实现。
type userService struct {
	repo repository.UserRepository // 用户数据访问实例
}

// NewUserService 创建一个新的用户业务逻辑实例。
func NewUserService(repo repository.UserRepository) UserService {
	return &userService{repo: repo}
}

// CreateUser 创建新用户。
func (s *userService) CreateUser(req *model.CreateUserRequest) (*model.User, error) {
	user := &model.User{
		Name: req.Name,
		Age:  req.Age,
	}
	err := s.repo.Create(user)
	if err != nil {
		return nil, err
	}
	return user, nil
}

// GetUserByID 根据 ID 查询用户。
func (s *userService) GetUserByID(id uint) (*model.User, error) {
	return s.repo.GetByID(id)
}

// GetAllUsers 查询所有用户。
func (s *userService) GetAllUsers() ([]model.User, error) {
	return s.repo.GetAll()
}

// UpdateUser 更新指定用户，仅更新请求中非空的字段。
func (s *userService) UpdateUser(id uint, req *model.UpdateUserRequest) (*model.User, error) {
	user, err := s.repo.GetByID(id)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, nil
	}

	if req.Name != "" {
		user.Name = req.Name
	}
	if req.Age != 0 {
		user.Age = req.Age
	}

	err = s.repo.Update(user)
	if err != nil {
		return nil, err
	}
	return user, nil
}

// DeleteUser 根据 ID 删除用户。
func (s *userService) DeleteUser(id uint) error {
	return s.repo.Delete(id)
}
