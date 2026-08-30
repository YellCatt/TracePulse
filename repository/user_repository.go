// Package repository 定义了数据访问层，封装数据库操作。
package repository

import (
	"github.com/example/tracepulse/logger"
	"github.com/example/tracepulse/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// UserRepository 用户数据访问接口，定义了用户的增删改查操作。
type UserRepository interface {
	Create(user *model.User) error
	GetByID(id uint) (*model.User, error)
	GetAll() ([]model.User, error)
	Update(user *model.User) error
	Delete(id uint) error
}

// userRepository UserRepository 的默认实现，基于 GORM 操作 SQLite。
type userRepository struct {
	db *gorm.DB // GORM 数据库连接实例
}

// NewUserRepository 创建一个新的用户数据访问实例。
func NewUserRepository(db *gorm.DB) UserRepository {
	return &userRepository{db: db}
}

// Create 创建新用户记录。
func (r *userRepository) Create(user *model.User) error {
	err := r.db.Create(user).Error
	if err != nil {
		logger.Error("create user failed",
			zap.String("name", user.Name), zap.Error(err))
		return err
	}
	logger.Debug("user persisted", zap.Uint("id", user.ID), zap.String("name", user.Name))
	return nil
}

// GetByID 根据 ID 查询用户，返回 nil,nil 表示未找到记录。
func (r *userRepository) GetByID(id uint) (*model.User, error) {
	var user model.User
	err := r.db.First(&user, id).Error
	if err == gorm.ErrRecordNotFound {
		logger.Debug("user not found in db", zap.Uint("id", id))
		return nil, nil
	}
	if err != nil {
		logger.Error("get user failed", zap.Uint("id", id), zap.Error(err))
		return nil, err
	}
	logger.Debug("user fetched from db", zap.Uint("id", id), zap.String("name", user.Name))
	return &user, nil
}

// GetAll 查询所有用户记录。
func (r *userRepository) GetAll() ([]model.User, error) {
	var users []model.User
	err := r.db.Find(&users).Error
	if err != nil {
		logger.Error("get all users failed", zap.Error(err))
		return nil, err
	}
	logger.Debug("all users fetched from db", zap.Int("count", len(users)))
	return users, nil
}

// Update 更新指定用户的所有字段。
func (r *userRepository) Update(user *model.User) error {
	err := r.db.Save(user).Error
	if err != nil {
		logger.Error("update user failed",
			zap.Uint("id", user.ID), zap.Error(err))
		return err
	}
	logger.Debug("user updated in db", zap.Uint("id", user.ID))
	return nil
}

// Delete 根据 ID 删除用户记录。
func (r *userRepository) Delete(id uint) error {
	err := r.db.Delete(&model.User{}, id).Error
	if err != nil {
		logger.Error("delete user failed", zap.Uint("id", id), zap.Error(err))
		return err
	}
	logger.Debug("user deleted from db", zap.Uint("id", id))
	return nil
}
