package master

import (
	"sync"

	"encoding/json"
	"fmt"
	"github.com/juju/errors"
	"github.com/tiglabs/baudstorage/proto"
	"github.com/tiglabs/baudstorage/util/log"
	"strings"
	"time"
)

type MetaReplica struct {
	Addr       string
	start      uint64
	end        uint64
	nodeId     uint64
	ReportTime int64
	Status     int8
	IsLeader   bool
	metaNode   *MetaNode
}

type MetaPartition struct {
	PartitionID      uint64
	Start            uint64
	End              uint64
	MaxNodeID        uint64
	Replicas         []*MetaReplica
	ReplicaNum       uint8
	Status           int8
	volName          string
	PersistenceHosts []string
	Peers            []proto.Peer
	MissNodes        map[string]int64
	sync.RWMutex
}

func NewMetaReplica(start, end uint64, metaNode *MetaNode) (mr *MetaReplica) {
	mr = &MetaReplica{start: start, end: end, nodeId: metaNode.ID, Addr: metaNode.Addr}
	mr.metaNode = metaNode
	mr.ReportTime = time.Now().Unix()
	return
}

func NewMetaPartition(partitionID, start, end uint64, replicaNum uint8, volName string) (mp *MetaPartition) {
	mp = &MetaPartition{PartitionID: partitionID, Start: start, End: end, volName: volName}
	mp.ReplicaNum = replicaNum
	mp.Replicas = make([]*MetaReplica, 0)
	mp.Status = proto.Unavaliable
	mp.MissNodes = make(map[string]int64, 0)
	mp.Peers = make([]proto.Peer, 0)
	mp.PersistenceHosts = make([]string, 0)
	return
}

func (mp *MetaPartition) toJson() (body []byte, err error) {
	mp.RLock()
	defer mp.RUnlock()
	return json.Marshal(mp)
}

func (mp *MetaPartition) setPeers(peers []proto.Peer) {
	mp.Peers = peers
}

func (mp *MetaPartition) setPersistenceHosts(hosts []string) {
	mp.PersistenceHosts = hosts
}

func (mp *MetaPartition) hostsToString() (hosts string) {
	return strings.Join(mp.PersistenceHosts, UnderlineSeparator)
}

func (mp *MetaPartition) addReplica(mr *MetaReplica) {
	for _, m := range mp.Replicas {
		if m.Addr == mr.Addr {
			return
		}
	}
	mp.Replicas = append(mp.Replicas, mr)
	return
}

func (mp *MetaPartition) removeReplica(mr *MetaReplica) {
	var newReplicas []*MetaReplica
	for _, m := range mp.Replicas {
		if m.Addr == mr.Addr {
			continue
		}
		newReplicas = append(newReplicas, m)
	}
	mp.Replicas = newReplicas
	return
}

func (mp *MetaPartition) removeReplicaByAddr(addr string) {
	var newReplicas []*MetaReplica
	for _, m := range mp.Replicas {
		if m.Addr == addr {
			continue
		}
		newReplicas = append(newReplicas, m)
	}
	mp.Replicas = newReplicas
	return
}

func (mp *MetaPartition) updateAllReplicasEnd() {
	for _, mr := range mp.Replicas {
		mr.end = mp.End
	}
}

func (mp *MetaPartition) UpdateEnd(c *Cluster, end uint64) {
	//to prevent overflow
	if end > (DefaultMaxMetaPartitionInodeID - DefaultMetaPartitionInodeIDStep) {
		log.LogWarnf("action[UpdateEnd] clusterID[%v] partitionID[%v] nextStart[%v] "+
			"to prevent overflow ,not update end", c.Name, mp.PartitionID, end)
		return
	}
	var err error
	tasks := make([]*proto.AdminTask, 0)
	oldEnd := mp.End
	mp.End = end
	t := mp.generateUpdateMetaReplicaTask(c.Name, mp.PartitionID, end)
	//if no leader,don't update end
	if t == nil {
		mp.End = oldEnd
		return
	}
	if err = c.syncUpdateMetaPartition(mp.volName, mp); err != nil {
		mp.End = oldEnd
		goto errDeal
	}
	mp.updateAllReplicasEnd()
	tasks = append(tasks, t)
	c.putMetaNodeTasks(tasks)
	if err = c.CreateMetaPartition(mp.volName, mp.End+1, DefaultMaxMetaPartitionInodeID); err != nil {
		Warn(c.Name, fmt.Sprintf("action[UpdateEnd] clusterID[%v] partitionID[%v] create meta partition err[%v]",
			c.Name, mp.PartitionID, err))
		goto errDeal
	}
	log.LogWarnf("action[UpdateEnd] partitionID[%v] end[%v] success", mp.PartitionID, mp.End)
	return
errDeal:
	log.LogErrorf("action[UpdateEnd] partitionID[%v] err[%v]", mp.PartitionID, err)
	return
}

func (mp *MetaPartition) checkEnd(c *Cluster, maxPartitionID uint64) {

	if mp.PartitionID < maxPartitionID {
		return
	}
	vol, err := c.getVol(mp.volName)
	if err != nil {
		log.LogWarnf("action[checkEnd] vol[%v] not exist", mp.volName)
		return
	}
	mp.Lock()
	defer mp.Unlock()
	curMaxPartitionID := vol.getMaxPartitionID()
	if mp.PartitionID != curMaxPartitionID {
		log.LogWarnf("action[checkEnd] partition[%v] not max partition[%v]", mp.PartitionID, curMaxPartitionID)
		return
	}
	if mp.End != DefaultMaxMetaPartitionInodeID {
		oldEnd := mp.End
		mp.End = DefaultMaxMetaPartitionInodeID
		if err := c.syncUpdateMetaPartition(mp.volName, mp); err != nil {
			mp.End = oldEnd
			log.LogErrorf("action[checkEnd] partitionID[%v] err[%v]", mp.PartitionID, err)
		}
	}
	log.LogWarnf("action[checkEnd] partitionID[%v] end[%v]", mp.PartitionID, mp.End)
}

func (mp *MetaPartition) getMetaReplica(addr string) (mr *MetaReplica, err error) {
	for _, mr = range mp.Replicas {
		if mr.Addr == addr {
			return
		}
	}
	return nil, metaReplicaNotFound(addr)
}

func (mp *MetaPartition) checkAndRemoveMissMetaReplica(addr string) {
	if _, ok := mp.MissNodes[addr]; ok {
		delete(mp.MissNodes, addr)
	}
}

func (mp *MetaPartition) checkStatus(writeLog bool, replicaNum int) {
	mp.Lock()
	defer mp.Unlock()
	liveReplicas := mp.getLiveReplica()
	if len(liveReplicas) <= replicaNum/2 {
		mp.Status = proto.Unavaliable
	} else {
		mr, err := mp.getLeaderMetaReplica()
		if err != nil {
			mp.Status = proto.Unavaliable
		}
		mp.Status = mr.Status
	}

	if writeLog {
		log.LogInfof("action[checkMPStatus],id:%v,status:%v,replicaNum:%v,liveReplicas:%v persistenceHosts:%v",
			mp.PartitionID, mp.Status, mp.ReplicaNum, len(liveReplicas), mp.PersistenceHosts)
	}
}

func (mp *MetaPartition) getLeaderMetaReplica() (mr *MetaReplica, err error) {
	for _, mr = range mp.Replicas {
		if mr.IsLeader {
			return
		}
	}
	err = NoLeader
	return
}

func (mp *MetaPartition) checkReplicaNum(c *Cluster, volName string, replicaNum uint8) {
	mp.RLock()
	defer mp.RUnlock()
	if mp.ReplicaNum != replicaNum {
		msg := fmt.Sprintf("FIX MetaPartition replicaNum clusterID[%v] vol[%v] replica num[%v],current num[%v]",
			c.Name, volName, replicaNum, mp.ReplicaNum)
		Warn(c.Name, msg)
	}
}

func (mp *MetaPartition) deleteExcessReplication() (excessAddr string, t *proto.AdminTask, err error) {
	mp.RLock()
	defer mp.RUnlock()
	for _, mr := range mp.Replicas {
		if !contains(mp.PersistenceHosts, mr.Addr) {
			t = mr.generateDeleteReplicaTask(mp.PartitionID)
			err = MetaReplicaExcessError
			break
		}
	}
	return
}

func (mp *MetaPartition) getLackReplication() (lackAddrs []string) {
	mp.RLock()
	defer mp.RUnlock()
	var liveReplicas []string
	for _, mr := range mp.Replicas {
		liveReplicas = append(liveReplicas, mr.Addr)
	}
	for _, host := range mp.PersistenceHosts {
		if !contains(liveReplicas, host) {
			lackAddrs = append(lackAddrs, host)
			break
		}
	}
	return
}

func (mp *MetaPartition) UpdateMetaPartition(mgr *proto.MetaPartitionReport, metaNode *MetaNode) {

	if !contains(mp.PersistenceHosts, metaNode.Addr) {
		return
	}
	mp.Lock()
	defer mp.Unlock()
	mr, err := mp.getMetaReplica(metaNode.Addr)
	if err != nil {
		mr = NewMetaReplica(mp.Start, mp.End, metaNode)
		mp.addReplica(mr)
	}
	mp.MaxNodeID = mgr.MaxInodeID
	mr.updateMetric(mgr)
	mp.checkAndRemoveMissMetaReplica(metaNode.Addr)
}

func (mp *MetaPartition) canOffline(nodeAddr string, replicaNum int) (err error) {
	liveReplicas := mp.getLiveReplica()
	if !mp.hasMajorityReplicas(len(liveReplicas), replicaNum) {
		err = NoHaveMajorityReplica
		return
	}
	liveAddrs := mp.getLiveReplicasAddr(liveReplicas)
	if len(liveReplicas) == (replicaNum/2+1) && contains(liveAddrs, nodeAddr) {
		err = fmt.Errorf("live replicas num will be less than majority after offline nodeAddr: %v", nodeAddr)
		return
	}
	return
}

func (mp *MetaPartition) hasMajorityReplicas(liveReplicas int, replicaNum int) bool {
	return liveReplicas >= int(mp.ReplicaNum/2+1)
}

func (mp *MetaPartition) getLiveReplicasAddr(liveReplicas []*MetaReplica) (addrs []string) {
	addrs = make([]string, 0)
	for _, mr := range liveReplicas {
		addrs = append(addrs, mr.Addr)
	}
	return
}
func (mp *MetaPartition) getLiveReplica() (liveReplicas []*MetaReplica) {
	liveReplicas = make([]*MetaReplica, 0)
	for _, mr := range mp.Replicas {
		if mr.isActive() {
			liveReplicas = append(liveReplicas, mr)
		}
	}
	return
}

func (mp *MetaPartition) updateInfoToStore(newHosts []string, newPeers []proto.Peer, volName string, c *Cluster) (err error) {
	oldHosts := make([]string, len(mp.PersistenceHosts))
	copy(oldHosts, mp.PersistenceHosts)
	oldPeers := make([]proto.Peer, len(mp.Peers))
	copy(oldPeers, mp.Peers)
	mp.PersistenceHosts = newHosts
	mp.Peers = newPeers
	if err = c.syncUpdateMetaPartition(volName, mp); err != nil {
		mp.PersistenceHosts = oldHosts
		mp.Peers = oldPeers
		log.LogWarnf("action[updateInfoToStore] failed,partitionID:%v  old hosts:%v new hosts:%v oldPeers:%v  newPeers:%v",
			mp.PartitionID, mp.PersistenceHosts, newHosts, mp.Peers, newPeers)
		return
	}
	log.LogWarnf("action[updateInfoToStore] success,partitionID:%v  old hosts:%v  new hosts:%v oldPeers:%v  newPeers:%v ",
		mp.PartitionID, oldHosts, mp.PersistenceHosts, oldPeers, mp.Peers)
	return
}

func (mp *MetaPartition) getLiveAddrs() (liveAddrs []string) {
	liveAddrs = make([]string, 0)
	for _, mr := range mp.Replicas {
		if mr.isActive() {
			liveAddrs = append(liveAddrs, mr.Addr)
		}
	}
	return liveAddrs
}

func (mp *MetaPartition) missedReplica(addr string) bool {
	return !contains(mp.getLiveAddrs(), addr)
}

func (mp *MetaPartition) needWarnMissReplica(addr string, warnInterval int64) (isWarn bool) {
	lastWarnTime, ok := mp.MissNodes[addr]
	if !ok {
		isWarn = true
		mp.MissNodes[addr] = time.Now().Unix()
	} else if (time.Now().Unix() - lastWarnTime) > warnInterval {
		isWarn = true
		mp.MissNodes[addr] = time.Now().Unix()
	}
	return false
}

func (mp *MetaPartition) checkReplicaMiss(clusterID string, partitionMissSec, warnInterval int64) {
	mp.Lock()
	defer mp.Unlock()
	//has report
	for _, replica := range mp.Replicas {
		if contains(mp.PersistenceHosts, replica.Addr) && replica.isMissed() && mp.needWarnMissReplica(replica.Addr, warnInterval) {
			metaNode := replica.metaNode
			var (
				lastReportTime time.Time
			)
			isActive := true
			if metaNode != nil {
				lastReportTime = metaNode.ReportTime
				isActive = metaNode.IsActive
			}
			msg := fmt.Sprintf("action[checkReplicaMiss], clusterID[%v] volName[%v] partition:%v  on Node:%v  "+
				"miss time > :%v  vlocLastRepostTime:%v   dnodeLastReportTime:%v  nodeisActive:%v",
				clusterID, mp.volName, mp.PartitionID, replica.Addr, partitionMissSec, replica.ReportTime, lastReportTime, isActive)
			Warn(clusterID, msg)
		}
	}
	// never report
	for _, addr := range mp.PersistenceHosts {
		if mp.missedReplica(addr) && mp.needWarnMissReplica(addr, warnInterval) {
			msg := fmt.Sprintf("action[checkReplicaMiss],clusterID[%v] volName[%v] partition:%v  on Node:%v  "+
				"miss time  > %v ",
				clusterID, mp.volName, mp.PartitionID, addr, DefaultMetaPartitionTimeOutSec)
			Warn(clusterID, msg)
		}
	}
}

func (mp *MetaPartition) GenerateReplicaTask(clusterID, volName string) (tasks []*proto.AdminTask) {
	var msg string
	tasks = make([]*proto.AdminTask, 0)
	if excessAddr, task, excessErr := mp.deleteExcessReplication(); excessErr != nil {
		msg = fmt.Sprintf("action[%v],clusterID[%v] metaPartition:%v  excess replication"+
			" on :%v  err:%v  persistenceHosts:%v",
			DeleteExcessReplicationErr, clusterID, mp.PartitionID, excessAddr, excessErr.Error(), mp.PersistenceHosts)
		log.LogWarn(msg)
		tasks = append(tasks, task)
	}
	if lackAddrs := mp.getLackReplication(); lackAddrs != nil {
		msg = fmt.Sprintf("action[getLackReplication],clusterID[%v] metaPartition:%v  lack replication"+
			" on :%v PersistenceHosts:%v",
			clusterID, mp.PartitionID, lackAddrs, mp.PersistenceHosts)
		log.LogWarn(msg)
		tasks = append(tasks, mp.generateAddLackMetaReplicaTask(lackAddrs, volName)...)
	}

	return
}

func (mp *MetaPartition) generateCreateMetaPartitionTasks(specifyAddrs []string, peers []proto.Peer, volName string) (tasks []*proto.AdminTask) {
	tasks = make([]*proto.AdminTask, 0)
	hosts := make([]string, 0)
	req := &proto.CreateMetaPartitionRequest{
		Start:       mp.Start,
		End:         mp.End,
		PartitionID: mp.PartitionID,
		Members:     peers,
		VolName:     volName,
	}
	if specifyAddrs == nil {
		hosts = mp.PersistenceHosts
	} else {
		hosts = specifyAddrs
	}

	for _, addr := range hosts {
		t := proto.NewAdminTask(proto.OpCreateMetaPartition, addr, req)
		resetMetaPartitionTaskID(t, mp.PartitionID)
		tasks = append(tasks, t)
	}
	return
}

func (mp *MetaPartition) generateAddLackMetaReplicaTask(addrs []string, volName string) (tasks []*proto.AdminTask) {
	return mp.generateCreateMetaPartitionTasks(addrs, mp.Peers, volName)
}

func (mp *MetaPartition) generateOfflineTask(volName string, removePeer proto.Peer, addPeer proto.Peer) (t *proto.AdminTask, err error) {
	mr, err := mp.getLeaderMetaReplica()
	if err != nil {
		return nil, errors.Trace(err)
	}
	req := &proto.MetaPartitionOfflineRequest{PartitionID: mp.PartitionID, VolName: volName, RemovePeer: removePeer, AddPeer: addPeer}
	t = proto.NewAdminTask(proto.OpOfflineMetaPartition, mr.Addr, req)
	resetMetaPartitionTaskID(t, mp.PartitionID)
	return
}

func resetMetaPartitionTaskID(t *proto.AdminTask, partitionID uint64) {
	t.ID = fmt.Sprintf("%v_pid[%v]", t.ID, partitionID)
}

func (mp *MetaPartition) generateUpdateMetaReplicaTask(clusterID string, partitionID uint64, end uint64) (t *proto.AdminTask) {
	mr, err := mp.getLeaderMetaReplica()
	if err != nil {
		msg := fmt.Sprintf("action[generateUpdateMetaReplicaTask] clusterID[%v] meta partition %v no leader",
			clusterID, mp.PartitionID)
		Warn(clusterID, msg)
		return
	}
	req := &proto.UpdateMetaPartitionRequest{PartitionID: partitionID, End: end, VolName: mp.volName}
	t = proto.NewAdminTask(proto.OpUpdateMetaPartition, mr.Addr, req)
	resetMetaPartitionTaskID(t, mp.PartitionID)
	return
}

func (mr *MetaReplica) generateDeleteReplicaTask(partitionID uint64) (t *proto.AdminTask) {
	req := &proto.DeleteMetaPartitionRequest{PartitionID: partitionID}
	t = proto.NewAdminTask(proto.OpDeleteMetaPartition, mr.Addr, req)
	resetMetaPartitionTaskID(t, partitionID)
	return
}

func (mr *MetaReplica) isMissed() (miss bool) {
	return time.Now().Unix()-mr.ReportTime > DefaultMetaPartitionTimeOutSec
}

func (mr *MetaReplica) isActive() (active bool) {
	return mr.metaNode.IsActive && mr.Status != proto.Unavaliable &&
		time.Now().Unix()-mr.ReportTime < DefaultMetaPartitionTimeOutSec
}

func (mr *MetaReplica) setLastReportTime() {
	mr.ReportTime = time.Now().Unix()
}

func (mr *MetaReplica) updateMetric(mgr *proto.MetaPartitionReport) {
	mr.Status = (int8)(mgr.Status)
	mr.IsLeader = mgr.IsLeader
	mr.setLastReportTime()
}

func (mp *MetaPartition) updateMetricByRaft(mpv *MetaPartitionValue) {
	mp.Lock()
	defer mp.Unlock()
	mp.Start = mpv.Start
	mp.End = mpv.End
	mp.Peers = mpv.Peers
	mp.PersistenceHosts = strings.Split(mpv.Hosts, UnderlineSeparator)

}
