package main

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

type memoryStatusEx struct {
	cbSize                  uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	globalMemoryStatus = kernel32.NewProc("GlobalMemoryStatusEx")
)

type CompatibilityReport struct {
	CPU      string `json:"cpu"`
	Cores    int    `json:"cores"`
	RAM      string `json:"ram"`
	RAMBytes uint64 `json:"ramBytes"`
	GPU      string `json:"gpu"`
	GPUBytes uint64 `json:"gpuBytes"`
	Score    string `json:"score"`
	RecMode  string `json:"recMode"`
}

func GetSystemDiagnostics() CompatibilityReport {
	cores := runtime.NumCPU()
	cpuName := getCPUName()

	ramBytes, err := getSystemMemory()
	if err != nil {
		ramBytes = 0
	}
	ramGB := float64(ramBytes) / (1024 * 1024 * 1024)

	gpuName, gpuBytes := getGPUDetails()

	ramStr := fmt.Sprintf("%.2f GB", ramGB)
	gpuStr := "None"
	if gpuName != "" {
		if gpuBytes > 0 {
			gpuStr = fmt.Sprintf("%s (%.2f GB VRAM)", gpuName, float64(gpuBytes)/(1024*1024*1024))
		} else {
			gpuStr = gpuName
		}
	}

	// Scoring Criteria
	score := "cloud_recommended"
	recMode := "light"

	hasGreatRAM := ramGB >= 15.5 // 16GB
	hasGreatCPU := cores >= 8
	hasGreatGPU := gpuBytes >= 4*1024*1024*1024 // 4GB VRAM

	hasLightRAM := ramGB >= 6.5 // 7-8GB
	hasLightCPU := cores >= 4

	if hasGreatRAM && hasGreatCPU && hasGreatGPU {
		score = "great_fit"
		recMode = "standard"
	} else if hasLightRAM && hasLightCPU {
		score = "light_mode"
		recMode = "light"
	}

	return CompatibilityReport{
		CPU:      fmt.Sprintf("%d Cores (%s)", cores, cpuName),
		Cores:    cores,
		RAM:      ramStr,
		RAMBytes: ramBytes,
		GPU:      gpuStr,
		GPUBytes: gpuBytes,
		Score:    score,
		RecMode:  recMode,
	}
}

func getSystemMemory() (uint64, error) {
	var ms memoryStatusEx
	ms.cbSize = uint32(unsafe.Sizeof(ms))
	r1, _, err := globalMemoryStatus.Call(uintptr(unsafe.Pointer(&ms)))
	if r1 == 0 {
		return 0, err
	}
	return ms.ullTotalPhys, nil
}

func getCPUName() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return "x86/x64 Processor"
	}
	defer k.Close()

	name, _, err := k.GetStringValue("ProcessorNameString")
	if err != nil {
		return "x86/x64 Processor"
	}
	return name
}

func getGPUDetails() (string, uint64) {
	classKey := `SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}`
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, classKey, registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return "", 0
	}
	defer k.Close()

	subkeys, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return "", 0
	}

	bestGPUName := ""
	var bestGPUBytes uint64 = 0

	for _, sub := range subkeys {
		// Subkeys are typically "0000", "0001", etc.
		if len(sub) != 4 {
			continue
		}

		subPath := classKey + `\` + sub
		sk, err := registry.OpenKey(registry.LOCAL_MACHINE, subPath, registry.QUERY_VALUE)
		if err != nil {
			continue
		}

		desc, _, err := sk.GetStringValue("DriverDesc")
		if err != nil {
			sk.Close()
			continue
		}

		// Read hardware information memory size if present
		var memBytes uint64 = 0
		valBytes, _, err := sk.GetBinaryValue("HardwareInformation.MemorySize")
		if err == nil && len(valBytes) >= 4 {
			// Read DWORD or QWORD
			if len(valBytes) == 8 {
				memBytes = *(*uint64)(unsafe.Pointer(&valBytes[0]))
			} else {
				memBytes = uint64(*(*uint32)(unsafe.Pointer(&valBytes[0])))
			}
		}

		// Try loading QWORD directly if registry value is stored as integer
		if memBytes == 0 {
			valInt, _, err := sk.GetIntegerValue("HardwareInformation.MemorySize")
			if err == nil {
				memBytes = valInt
			}
		}

		if memBytes > bestGPUBytes || bestGPUName == "" {
			bestGPUName = desc
			bestGPUBytes = memBytes
		}
		sk.Close()
	}

	return bestGPUName, bestGPUBytes
}
