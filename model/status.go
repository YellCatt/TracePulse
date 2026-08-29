package model

// CpuStatus CPU 状态信息。
type CpuStatus struct {
	Usage     float64 `json:"usage"`      // CPU 使用率（百分比）
	Count     int     `json:"count"`      // CPU 核心数
	Vendor    string  `json:"vendor"`     // CPU 厂商
	Model     string  `json:"model"`      // CPU 型号
	Mhz       float64 `json:"mhz"`        // CPU 主频（MHz）
	CacheSize int     `json:"cache_size"` // CPU 缓存大小（KB）
}

// MemoryStatus 内存状态信息。
type MemoryStatus struct {
	Total     float64 `json:"total"`     // 内存总量（KB）
	Used      float64 `json:"used"`      // 已用内存（KB）
	Free      float64 `json:"free"`      // 空闲内存（KB）
	Available float64 `json:"available"` // 可用内存（KB）
	Usage     float64 `json:"usage"`     // 内存使用率（百分比）
}

// NetworkStatus 网络状态信息，含收发字节数及速率。
type NetworkStatus struct {
	BytesRecv float64 `json:"bytes_recv"` // 累计接收字节数（KB）
	BytesSent float64 `json:"bytes_sent"` // 累计发送字节数（KB）
	RecvSpeed float64 `json:"recv_speed"` // 接收速率（KB/s）
	SendSpeed float64 `json:"send_speed"` // 发送速率（KB/s）
}

// DiskStatus 单个磁盘/分区的状态信息，含容量及读写速率。
type DiskStatus struct {
	Mountpoint string  `json:"mountpoint"` // 挂载点/盘符
	Total      float64 `json:"total"`      // 磁盘总量（KB）
	Used       float64 `json:"used"`       // 已用空间（KB）
	Free       float64 `json:"free"`       // 剩余空间（KB）
	Usage      float64 `json:"usage"`      // 磁盘使用率（百分比）
	ReadSpeed  float64 `json:"read_speed"` // 读取速率（KB/s）
	WriteSpeed float64 `json:"write_speed"` // 写入速率（KB/s）
}

// StatusUnits 状态信息中各字段的单位描述。
type StatusUnits struct {
	Cpu         string `json:"cpu"`          // CPU 数量单位
	CpuUsage    string `json:"cpu_usage"`    // CPU 使用率单位
	Memory      string `json:"memory"`       // 内存容量单位
	MemoryUsage string `json:"memory_usage"` // 内存使用率单位
	Network     string `json:"network"`      // 网络流量单位
	Speed       string `json:"speed"`        // 速率单位
	Disk        string `json:"disk"`         // 磁盘容量单位
	DiskUsage   string `json:"disk_usage"`   // 磁盘使用率单位
	Uptime      string `json:"uptime"`       // 运行时间单位
}

// SystemStatus 系统整体运行状态，聚合 CPU、内存、网络、磁盘等信息。
type SystemStatus struct {
	Cpu     CpuStatus     `json:"cpu"`     // CPU 状态
	Memory  MemoryStatus  `json:"memory"`  // 内存状态
	Network NetworkStatus `json:"network"` // 网络状态
	Disk    []DiskStatus  `json:"disk"`    // 各磁盘/分区状态列表
	Uptime  uint64        `json:"uptime"`  // 系统运行时长（秒）
	Units   StatusUnits   `json:"units"`   // 各字段单位描述
}
