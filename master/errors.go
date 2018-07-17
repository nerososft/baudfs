package master

import (
	"fmt"

	"github.com/juju/errors"
)

var (
	NoAvailDataPartition  = errors.New("no avail data partition")
	DataPartitionNotFound = errors.New("data partition not found")
	RackNotFound          = errors.New("rack not found")
	DataNodeNotFound      = errors.New("data node not found")
	MetaNodeNotFound      = errors.New("meta node not found")
	VolNotFound           = errors.New("vol not found")
	MetaPartitionNotFound = errors.New("meta partition not found")
	DataReplicaNotFound   = errors.New("data replica not found")
	UnMatchPara           = errors.New("para not unmatched")

	DisOrderArrayErr                    = errors.New("dis order array is nil")
	DataReplicaExcessError              = errors.New("data replica Excess error")
	DataReplicaLackError                = errors.New("data replica Lack error")
	DataReplicaHasMissOneError          = errors.New("data replica has miss one ,cannot miss any one")
	NoHaveAnyDataNodeToWrite            = errors.New("No have any data node for create data partition")
	NoHaveAnyMetaNodeToWrite            = errors.New("No have any meta node for create meta partition")
	CannotOffLineErr                    = errors.New("cannot offline because avail data replica <0")
	NoAnyDataNodeForCreateDataPartition = errors.New("no have enough data server for create data partition")
	NoRackForCreateDataPartition        = errors.New("no rack for create data partition")
	NoAnyMetaNodeForCreateMetaPartition = errors.New("no have enough meta server for create meta partition")
	MetaReplicaExcessError              = errors.New("meta partition Replication Excess error")
	NoHaveMajorityReplica               = errors.New("no have majority replica error")
	NoLeader                            = errors.New("no leader")
	ErrBadConfFile                      = errors.New("BadConfFile")
	InvalidDataPartitionType            = errors.New("invalid data partition type. extent or tiny")
	ParaEnableNotFound                  = errors.New("para enable not found")
)

func paraNotFound(name string) (err error) {
	return errors.Errorf("parameter %v not found", name)
}

func elementNotFound(name string) (err error) {
	return errors.Errorf("%v not found", name)
}

func metaPartitionNotFound(id uint64) (err error) {
	return elementNotFound(fmt.Sprintf("meta partition %v", id))
}

func dataPartitionNotFound(id uint64) (err error) {
	return elementNotFound(fmt.Sprintf("data partition %v", id))
}

func metaReplicaNotFound(addr string) (err error) {
	return elementNotFound(fmt.Sprintf("meta replica %v", addr))
}

func hasExist(name string) (err error) {
	err = errors.Errorf("%v has exist", name)
	return
}
