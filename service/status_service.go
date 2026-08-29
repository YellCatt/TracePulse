package service

import (
	"sync"
	"time"

	"github.com/example/gapi/model"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
)

// StatusService 系统状态业务逻辑接口，提供实时系统监控数据。
type StatusService interface {
	GetStatus() (*model.SystemStatus, error)
}

// statusService StatusService 的默认实现，通过后台 goroutine 定时采样系统指标。
type statusService struct {
	mu     sync.RWMutex        // 读写锁，保护 status 并发访问
	status *model.SystemStatus // 最新的系统状态快照
}

// NewStatusService 创建一个新的系统状态服务实例，并立即启动后台采样协程。
func NewStatusService() StatusService {
	s := &statusService{
		status: &model.SystemStatus{},
	}
	go s.sampler()
	return s
}

// sampler 后台定时采集系统指标（CPU、内存、网络、磁盘等），每秒采样一次。
func (s *statusService) sampler() {
	var lastNetRecv, lastNetSent uint64
	var lastDiskRead, lastDiskWrite uint64
	var lastSample time.Time

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	bootstrap := true
	for range ticker.C {
		now := time.Now()

		cpuInfo, _ := cpu.Info()
		cpuPercents, _ := cpu.Percent(0, false)
		memInfo, _ := mem.VirtualMemory()
		hostInfo, _ := host.Info()

		netCounters, _ := net.IOCounters(false)
		var recv, sent uint64
		for _, n := range netCounters {
			recv += n.BytesRecv
			sent += n.BytesSent
		}

		diskCounters, _ := disk.IOCounters()
		var dRead, dWrite uint64
		for _, d := range diskCounters {
			dRead += d.ReadBytes
			dWrite += d.WriteBytes
		}

		// 分别统计每个分区/硬盘的容量
		partitions, _ := disk.Partitions(true)
		diskStatuses := make([]model.DiskStatus, 0, len(partitions))
		for _, p := range partitions {
			usage, err := disk.Usage(p.Mountpoint)
			if err != nil || usage == nil || usage.Total == 0 {
				continue
			}
			diskStatuses = append(diskStatuses, model.DiskStatus{
				Mountpoint: p.Mountpoint,
				Total:      float64(usage.Total) / 1024,
				Used:       float64(usage.Used) / 1024,
				Free:       float64(usage.Free) / 1024,
				Usage:      usage.UsedPercent,
			})
		}

		s.mu.Lock()
		if bootstrap {
			lastNetRecv = recv
			lastNetSent = sent
			lastDiskRead = dRead
			lastDiskWrite = dWrite
			lastSample = now
			bootstrap = false
			s.mu.Unlock()
			continue
		}

		elapsed := now.Sub(lastSample).Seconds()
		if elapsed <= 0 {
			elapsed = 1
		}

		status := &model.SystemStatus{}

		cpuCount, _ := cpu.Counts(true)
		if len(cpuInfo) > 0 {
			status.Cpu = model.CpuStatus{
				Count:     cpuCount,
				Vendor:    cpuInfo[0].VendorID,
				Model:     cpuInfo[0].Model,
				Mhz:       cpuInfo[0].Mhz,
				CacheSize: int(cpuInfo[0].CacheSize),
			}
		}
		if len(cpuPercents) > 0 {
			status.Cpu.Usage = cpuPercents[0]
		}

		if memInfo != nil {
			status.Memory = model.MemoryStatus{
				Total:     float64(memInfo.Total) / 1024,
				Used:      float64(memInfo.Used) / 1024,
				Free:      float64(memInfo.Free) / 1024,
				Available: float64(memInfo.Available) / 1024,
				Usage:     memInfo.UsedPercent,
			}
		}

		status.Network = model.NetworkStatus{
			BytesRecv: float64(recv) / 1024,
			BytesSent: float64(sent) / 1024,
			RecvSpeed: float64(recv-lastNetRecv) / elapsed / 1024,
			SendSpeed: float64(sent-lastNetSent) / elapsed / 1024,
		}

		// 将全局读写速率附加到每个磁盘（后续可按设备名细分）
		readSpeed := float64(dRead-lastDiskRead) / elapsed / 1024
		writeSpeed := float64(dWrite-lastDiskWrite) / elapsed / 1024
		for i := range diskStatuses {
			diskStatuses[i].ReadSpeed = readSpeed
			diskStatuses[i].WriteSpeed = writeSpeed
		}
		status.Disk = diskStatuses

		if hostInfo != nil {
			status.Uptime = hostInfo.Uptime
		}

		status.Units = model.StatusUnits{
			Cpu:         "count",
			CpuUsage:    "percent",
			Memory:      "KB",
			MemoryUsage: "percent",
			Network:     "KB",
			Speed:       "KB/s",
			Disk:        "KB",
			DiskUsage:   "percent",
			Uptime:      "seconds",
		}

		s.status = status
		lastNetRecv = recv
		lastNetSent = sent
		lastDiskRead = dRead
		lastDiskWrite = dWrite
		lastSample = now
		s.mu.Unlock()
	}
}

// GetStatus 获取当前系统状态快照，线程安全。
func (s *statusService) GetStatus() (*model.SystemStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.status == nil {
		return &model.SystemStatus{}, nil
	}
	return s.status, nil
}
