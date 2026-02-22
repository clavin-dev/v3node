package node

import (
	"time"

	panel "github.com/clavin-dev/v3node/api/v2board"
	log "github.com/sirupsen/logrus"
)

func (c *Controller) reportUserTrafficTask() (err error) {
	var reportmin = 0
	var devicemin = 0
	var attemptedReport = false
	var reportFailed = false
	if c.info.Common.BaseConfig != nil {
		reportmin = c.info.Common.BaseConfig.NodeReportMinTraffic
		devicemin = c.info.Common.BaseConfig.DeviceOnlineMinTraffic
	}
	userTraffic, err := c.server.GetUserTrafficSlice(c.tag, reportmin)
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Info("Get user traffic failed")
		return nil
	}
	if len(userTraffic) > 0 {
		attemptedReport = true
		err = c.apiClient.ReportUserTraffic(userTraffic)
		if err != nil {
			reportFailed = true
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Info("Report user traffic failed")
		} else {
			log.WithField("tag", c.tag).Infof("Report %d users traffic", len(userTraffic))
			//log.WithField("tag", c.tag).Debugf("User traffic: %+v", userTraffic)
		}
	}

	if onlineDevice, err := c.limiter.GetOnlineDevice(); err != nil {
		log.Print(err)
	} else if len(*onlineDevice) > 0 {
		attemptedReport = true
		result := make([]panel.OnlineUser, 0, len(*onlineDevice))
		nocountUID := make(map[int]struct{}, len(userTraffic))
		for _, traffic := range userTraffic {
			total := traffic.Upload + traffic.Download
			if total < int64(devicemin*1000) {
				nocountUID[traffic.UID] = struct{}{}
			}
		}
		for _, online := range *onlineDevice {
			if _, ok := nocountUID[online.UID]; !ok {
				result = append(result, online)
			}
		}
		data := make(map[int][]string, len(result))
		for _, onlineuser := range result {
			// json structure: { UID1:["ip1","ip2"],UID2:["ip3","ip4"] }
			data[onlineuser.UID] = append(data[onlineuser.UID], onlineuser.IP)
		}
		if err = c.apiClient.ReportNodeOnlineUsers(&data); err != nil {
			reportFailed = true
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Info("Report online users failed")
		} else {
			log.WithField("tag", c.tag).Infof("Total %d online users, %d Reported", len(*onlineDevice), len(result))
			//log.WithField("tag", c.tag).Debugf("Online users: %+v", data)
		}
	}
	if attemptedReport {
		if reportFailed {
			grace := c.reportFailureGraceDuration()
			c.limiter.SetReportFailureGrace(grace)
			log.WithFields(log.Fields{
				"tag":   c.tag,
				"grace": grace.String(),
			}).Warn("Report failed, temporarily relaxed alive-based device limit")
		} else {
			c.limiter.ClearReportFailureGrace()
		}
	}

	userTraffic = nil
	return nil
}

func (c *Controller) reportFailureGraceDuration() time.Duration {
	grace := c.info.PushInterval * 3
	if grace < 30*time.Second {
		grace = 30 * time.Second
	}
	if grace > 10*time.Minute {
		grace = 10 * time.Minute
	}
	return grace
}

func compareUserList(old, new []panel.UserInfo) (deleted, added []panel.UserInfo) {
	oldIndex := make(map[string]int, len(old))
	for i := range old {
		oldIndex[old[i].Uuid] = i
	}

	for _, user := range new {
		idx, exists := oldIndex[user.Uuid]
		if !exists {
			added = append(added, user)
			continue
		}
		oldUser := old[idx]
		if oldUser.SpeedLimit != user.SpeedLimit || oldUser.DeviceLimit != user.DeviceLimit {
			deleted = append(deleted, oldUser)
			added = append(added, user)
		}
		delete(oldIndex, user.Uuid)
	}

	if len(oldIndex) > 0 {
		if deleted == nil {
			deleted = make([]panel.UserInfo, 0, len(oldIndex))
		}
		for _, idx := range oldIndex {
			deleted = append(deleted, old[idx])
		}
	}

	return deleted, added
}
