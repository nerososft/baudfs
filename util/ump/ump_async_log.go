package ump

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
)

type FunctionTp struct {
	Time         string `json:"time"`
	Key          string `json:"key"`
	HostName     string `json:"hostname"`
	ProcessState string `json:"processState"`
	ElapsedTime  string `json:"elapsedTime"`
}

type SystemAlive struct {
	Key      string `json:"key"`
	HostName string `json:"hostname"`
	Time     string `json:"time"`
}

type BusinessAlarm struct {
	Time         string `json:"time"`
	Key          string `json:"key"`
	HostName     string `json:"hostname"`
	BusinessType string `json:"type"`
	Value        string `json:"value"`
	Detail       string `json:"detail"`
}

const (
	LogDir              = "/export/home/tomcat/UMP-Monitor/logs/"
	FunctionTpSufixx    = "tp.log"
	SystemAliveSufixx   = "alive.log"
	BusinessAlarmSufixx = "business.log"
	LogFileOpt          = os.O_RDWR | os.O_CREATE | os.O_APPEND
	ChSize              = 102400
	BusinessAlarmType   = "BusinessAlarm"
	SystemAliveType     = "SystemAlive"
	FunctionTpType      = "FunctionTp"
	HostNameFile        = "/proc/sys/kernel/hostname"
	MaxLogSize          = 1024 * 1024 * 10
)

var (
	FunctionTpLogWrite    = &LogWrite{logCh: make(chan interface{}, ChSize)}
	SystemAliveLogWrite   = &LogWrite{logCh: make(chan interface{}, ChSize)}
	BusinessAlarmLogWrite = &LogWrite{logCh: make(chan interface{}, ChSize)}
)

type LogWrite struct {
	logCh     chan interface{}
	logName   string
	logSize   int64
	seq       int
	logSufixx string
	logFp     *os.File
	sigCh     chan bool
}

func (lw *LogWrite) initLogFp(sufixx string) (err error) {
	var fi os.FileInfo
	lw.seq = 0
	lw.sigCh = make(chan bool, 1)
	lw.logSufixx = sufixx
	lw.logName = fmt.Sprintf("%s%s%s", LogDir, "ump_", lw.logSufixx)
	if lw.logFp, err = os.OpenFile(lw.logName, LogFileOpt, 0666); err != nil {
		return
	}
	if fi, err = lw.logFp.Stat(); err != nil {
		return
	}
	lw.logSize = fi.Size()

	return
}

func (lw *LogWrite) backGroundCheckFile() (err error) {
	if lw.logSize <= MaxLogSize {
		return
	}
	lw.logFp.Close()
	lw.seq++
	if lw.seq > 3 {
		lw.seq = 1
	}

	name := fmt.Sprintf("%s%s%s.%d", LogDir, "ump_", lw.logSufixx, lw.seq)
	if _, err = os.Stat(name); err == nil {
		os.Remove(name)
	}
	os.Rename(lw.logName, name)

	if lw.logFp, err = os.OpenFile(lw.logName, LogFileOpt, 0666); err != nil {
		lw.seq--
		return
	}
	if err = os.Truncate(lw.logName, 0); err != nil {
		lw.seq--
		return
	}
	lw.logSize = 0

	return
}

func (lw *LogWrite) backGroundWrite(umpType string) {
	var (
		body []byte
	)

	for {
		obj := <-lw.logCh
		switch umpType {
		case FunctionTpType:
			tp := obj.(*FunctionTp)
			body, _ = json.Marshal(tp)
			FunctionTpPool.Put(tp)
		case SystemAliveType:
			alive := obj.(*SystemAlive)
			body, _ = json.Marshal(alive)
			SystemAlivePool.Put(alive)
		case BusinessAlarmType:
			alarm := obj.(*BusinessAlarm)
			body, _ = json.Marshal(alarm)
			AlarmPool.Put(alarm)
		}
		if lw.backGroundCheckFile() != nil {
			continue
		}
		body = append(body, []byte("\n")...)
		lw.logFp.Write(body)
		lw.logSize += (int64)(len(body))
	}
}

func initLogName(module string) (err error) {
	if err = os.MkdirAll(LogDir, 0666); err != nil {
		return
	}

	if HostName, err = GetLocalIpAddr(); err != nil {
		return
	}

	if err = FunctionTpLogWrite.initLogFp(module + "_" + FunctionTpSufixx); err != nil {
		return
	}

	if err = SystemAliveLogWrite.initLogFp(module + "_" + SystemAliveSufixx); err != nil {
		return
	}

	if err = BusinessAlarmLogWrite.initLogFp(module + "_" + BusinessAlarmSufixx); err != nil {
		return
	}

	return
}

func GetLocalIpAddr() (ipaddr string, err error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String(), nil
			}
		}
	}
	return "", fmt.Errorf("cannot get local ip")
}

func backGroudWrite() {
	go FunctionTpLogWrite.backGroundWrite(FunctionTpType)
	go SystemAliveLogWrite.backGroundWrite(SystemAliveType)
	go BusinessAlarmLogWrite.backGroundWrite(BusinessAlarmType)
}
