package limiter

import (
	"errors"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/juju/ratelimit"
	panel "github.com/clavin-dev/v3node/api/v2board"
	"github.com/clavin-dev/v3node/common/format"
)

var limitLock sync.RWMutex
var limiter map[string]*Limiter

func Init() {
	limiter = map[string]*Limiter{}
}

type Limiter struct {
	DomainRules   []*regexp.Regexp
	ProtocolRules []string
	SpeedLimit    int
	UserOnlineIP  *sync.Map      // Key: TagUUID, value: {Key: Ip, value: Uid}
	OldUserOnline *sync.Map      // Key: Ip, value: Uid
	UUIDtoUID     map[string]int // Key: UUID, value: Uid
	UserLimitInfo *sync.Map      // Key: TagUUID value: UserLimitInfo
	SpeedLimiter  *sync.Map      // key: TagUUID, value: *ratelimit.Bucket
	aliveLock     sync.RWMutex
	AliveList     map[int]int // Key: Uid, value: alive_ip
	// Unix timestamp. If now <= reportFailureUntil, skip alive-based device-limit reject.
	reportFailureUntil atomic.Int64
}

type UserLimitInfo struct {
	UID               int
	SpeedLimit        int
	DeviceLimit       int
	DynamicSpeedLimit int
	ExpireTime        int64
	OverLimit         bool
}

func AddLimiter(tag string, users []panel.UserInfo, aliveList map[int]int) *Limiter {
	alive := make(map[int]int, len(aliveList))
	for uid, count := range aliveList {
		alive[uid] = count
	}
	info := &Limiter{
		UserOnlineIP:  new(sync.Map),
		UserLimitInfo: new(sync.Map),
		SpeedLimiter:  new(sync.Map),
		AliveList:     alive,
		OldUserOnline: new(sync.Map),
	}
	uuidmap := make(map[string]int)
	for i := range users {
		uuidmap[users[i].Uuid] = users[i].Id
		userLimit := &UserLimitInfo{}
		userLimit.UID = users[i].Id
		if users[i].SpeedLimit != 0 {
			userLimit.SpeedLimit = users[i].SpeedLimit
		}
		if users[i].DeviceLimit != 0 {
			userLimit.DeviceLimit = users[i].DeviceLimit
		}
		userLimit.OverLimit = false
		info.UserLimitInfo.Store(format.UserTag(tag, users[i].Uuid), userLimit)
	}
	info.UUIDtoUID = uuidmap
	limitLock.Lock()
	limiter[tag] = info
	limitLock.Unlock()
	return info
}

func GetLimiter(tag string) (info *Limiter, err error) {
	limitLock.RLock()
	info, ok := limiter[tag]
	limitLock.RUnlock()
	if !ok {
		return nil, errors.New("not found")
	}
	return info, nil
}

func DeleteLimiter(tag string) {
	limitLock.Lock()
	delete(limiter, tag)
	limitLock.Unlock()
}

func (l *Limiter) UpdateUser(tag string, added []panel.UserInfo, deleted []panel.UserInfo) {
	for i := range deleted {
		l.UserLimitInfo.Delete(format.UserTag(tag, deleted[i].Uuid))
		l.UserOnlineIP.Delete(format.UserTag(tag, deleted[i].Uuid))
		l.SpeedLimiter.Delete(format.UserTag(tag, deleted[i].Uuid))
		delete(l.UUIDtoUID, deleted[i].Uuid)
		l.aliveLock.Lock()
		delete(l.AliveList, deleted[i].Id)
		l.aliveLock.Unlock()
	}
	for i := range added {
		userLimit := &UserLimitInfo{
			UID: added[i].Id,
		}
		if added[i].SpeedLimit != 0 {
			userLimit.SpeedLimit = added[i].SpeedLimit
			userLimit.ExpireTime = 0
		}
		if added[i].DeviceLimit != 0 {
			userLimit.DeviceLimit = added[i].DeviceLimit
		}
		userLimit.OverLimit = false
		l.UserLimitInfo.Store(format.UserTag(tag, added[i].Uuid), userLimit)
		l.UUIDtoUID[added[i].Uuid] = added[i].Id
	}
}

func (l *Limiter) UpdateDynamicSpeedLimit(tag, uuid string, limit int, expire time.Time) error {
	if v, ok := l.UserLimitInfo.Load(format.UserTag(tag, uuid)); ok {
		info := v.(*UserLimitInfo)
		info.DynamicSpeedLimit = limit
		info.ExpireTime = expire.Unix()
	} else {
		return errors.New("not found")
	}
	return nil
}

func (l *Limiter) CheckLimit(taguuid string, ip string, isTcp bool, noSSUDP bool) (Bucket *ratelimit.Bucket, Reject bool) {
	// check if ipv4 mapped ipv6
	ip = strings.TrimPrefix(ip, "::ffff:")
	nowUnix := time.Now().Unix()

	// check and gen speed limit Bucket
	nodeLimit := l.SpeedLimit
	userLimit := 0
	deviceLimit := 0
	var uid int
	if v, ok := l.UserLimitInfo.Load(taguuid); ok {
		u := v.(*UserLimitInfo)
		deviceLimit = u.DeviceLimit
		uid = u.UID
		if u.ExpireTime < nowUnix && u.ExpireTime != 0 {
			if u.SpeedLimit != 0 {
				userLimit = u.SpeedLimit
				u.DynamicSpeedLimit = 0
				u.ExpireTime = 0
			} else {
				l.UserLimitInfo.Delete(taguuid)
			}
		} else {
			userLimit = determineSpeedLimit(u.SpeedLimit, u.DynamicSpeedLimit)
		}
	} else {
		return nil, true
	}
	if noSSUDP {
		// Store online user for device limit
		newipMap := new(sync.Map)
		newipMap.Store(ip, uid)
		l.aliveLock.RLock()
		aliveIp := l.AliveList[uid]
		l.aliveLock.RUnlock()
		enforceAliveDeviceLimit := nowUnix > l.reportFailureUntil.Load()
		// If any device is online
		if v, loaded := l.UserOnlineIP.LoadOrStore(taguuid, newipMap); loaded {
			oldipMap := v.(*sync.Map)
			// If this is a new ip
			if _, loaded := oldipMap.LoadOrStore(ip, uid); !loaded {
				if v, loaded := l.OldUserOnline.Load(ip); loaded {
					if v.(int) == uid {
						l.OldUserOnline.Delete(ip)
					}
				} else if deviceLimit > 0 && enforceAliveDeviceLimit {
					if deviceLimit <= aliveIp {
						oldipMap.Delete(ip)
						return nil, true
					}
				}
			}
		} else if v, ok := l.OldUserOnline.Load(ip); ok {
			if v.(int) == uid {
				l.OldUserOnline.Delete(ip)
			}
		} else {
			if deviceLimit > 0 && enforceAliveDeviceLimit {
				if deviceLimit <= aliveIp {
					l.UserOnlineIP.Delete(taguuid)
					return nil, true
				}
			}
		}
	}

	limit := int64(determineSpeedLimit(nodeLimit, userLimit)) * 1000000 / 8 // If you need the Speed limit
	if limit > 0 {
		if v, ok := l.SpeedLimiter.Load(taguuid); ok {
			return v.(*ratelimit.Bucket), false
		}
		Bucket = ratelimit.NewBucketWithQuantum(time.Second, limit, limit) // Byte/s
		if v, loaded := l.SpeedLimiter.LoadOrStore(taguuid, Bucket); loaded {
			return v.(*ratelimit.Bucket), false
		}
		return Bucket, false
	} else {
		return nil, false
	}
}

func (l *Limiter) SetAliveList(alive map[int]int) {
	newAlive := make(map[int]int, len(alive))
	for uid, count := range alive {
		newAlive[uid] = count
	}
	l.aliveLock.Lock()
	l.AliveList = newAlive
	l.aliveLock.Unlock()
}

func (l *Limiter) SetReportFailureGrace(d time.Duration) {
	if d <= 0 {
		return
	}
	until := time.Now().Add(d).Unix()
	for {
		old := l.reportFailureUntil.Load()
		if until <= old {
			return
		}
		if l.reportFailureUntil.CompareAndSwap(old, until) {
			return
		}
	}
}

func (l *Limiter) ClearReportFailureGrace() {
	l.reportFailureUntil.Store(0)
}

func (l *Limiter) InReportFailureGrace() bool {
	return time.Now().Unix() <= l.reportFailureUntil.Load()
}

func (l *Limiter) GetOnlineDevice() (*[]panel.OnlineUser, error) {
	var onlineUser []panel.OnlineUser
	l.OldUserOnline = new(sync.Map)
	l.UserOnlineIP.Range(func(key, value interface{}) bool {
		taguuid := key.(string)
		ipMap := value.(*sync.Map)
		ipMap.Range(func(key, value interface{}) bool {
			uid := value.(int)
			ip := key.(string)
			l.OldUserOnline.Store(ip, uid)
			onlineUser = append(onlineUser, panel.OnlineUser{UID: uid, IP: ip})
			return true
		})
		l.UserOnlineIP.Delete(taguuid) // Reset online device
		return true
	})

	return &onlineUser, nil
}

type UserIpList struct {
	Uid    int      `json:"Uid"`
	IpList []string `json:"Ips"`
}
