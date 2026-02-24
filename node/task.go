package node

import (
	"runtime/debug"
	"time"

	panel "github.com/clavin-dev/v3node/api/v2board"
	"github.com/clavin-dev/v3node/common/task"
	vCore "github.com/clavin-dev/v3node/core"
	log "github.com/sirupsen/logrus"
)

func (c *Controller) startTasks(node *panel.NodeInfo) {
	// fetch node info task
	c.nodeInfoMonitorPeriodic = &task.Task{
		Name:                   "nodeInfoMonitor",
		Interval:               node.PullInterval,
		Execute:                c.nodeInfoMonitor,
		Reload:                 c.reloadTask,
		TimeoutReloadThreshold: 3,
	}
	// fetch user list task
	c.userReportPeriodic = &task.Task{
		Name:                   "reportUserTrafficTask",
		Interval:               node.PushInterval,
		Execute:                c.reportUserTrafficTask,
		Reload:                 c.reloadTask,
		DisableReloadOnTimeout: true,
	}
	log.WithField("tag", c.tag).Info("Start monitor node status")
	// delay to start nodeInfoMonitor
	_ = c.nodeInfoMonitorPeriodic.Start(false)
	log.WithField("tag", c.tag).Info("Start report node status")
	_ = c.userReportPeriodic.Start(false)
	if node.Security == panel.Tls {
		switch c.info.Common.CertInfo.CertMode {
		case "none", "", "file", "self":
		default:
			c.renewCertPeriodic = &task.Task{
				Name:                   "renewCertTask",
				Interval:               time.Hour * 24,
				Execute:                c.renewCertTask,
				Reload:                 c.reloadTask,
				DisableReloadOnTimeout: true,
			}
			log.WithField("tag", c.tag).Info("Start renew cert")
			// delay to start renewCert
			_ = c.renewCertPeriodic.Start(true)
		}
	}
}

func (c *Controller) reloadTask() {
	c.reloadLock.Lock()
	defer c.reloadLock.Unlock()

	if c.conf == nil {
		log.WithField("tag", c.tag).Error("Tasks reload skipped: node config is nil")
		return
	}
	if c.server == nil {
		log.WithField("tag", c.tag).Warn("Tasks reload skipped: server is nil")
		return
	}

	now := time.Now()
	cooldown := c.taskReloadCooldown
	if cooldown <= 0 {
		cooldown = 20 * time.Second
	}
	if !c.lastTaskReload.IsZero() && now.Sub(c.lastTaskReload) < cooldown {
		log.WithFields(log.Fields{
			"tag":      c.tag,
			"cooldown": cooldown.String(),
		}).Warn("Tasks reload skipped due to cooldown")
		return
	}
	c.lastTaskReload = now

	newClient, err := panel.New(c.conf)
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Tasks reload failed")
		return
	}
	c.apiClient = newClient
	if c.nodeInfoMonitorPeriodic != nil {
		c.nodeInfoMonitorPeriodic.Close()
	}
	if c.userReportPeriodic != nil {
		c.userReportPeriodic.Close()
	}
	if c.renewCertPeriodic != nil {
		c.renewCertPeriodic.Close()
	}
	if c.info == nil {
		log.WithField("tag", c.tag).Error("Tasks reload failed: node info is nil")
		return
	}
	c.startTasks(c.info)
}

func (c *Controller) nodeInfoMonitor() (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": r,
			}).Errorf("nodeInfoMonitor panic recovered\n%s", debug.Stack())
		}
	}()

	if c.apiClient == nil || c.server == nil || c.limiter == nil {
		return nil
	}

	// get node info
	newN, err := c.apiClient.GetNodeInfo()
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get node info failed")
		return nil
	}
	if newN != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
		}).Error("Got new node info, reload")
		// Non-blocking signal to avoid goroutine stuck when channel is full or nil
		if c.server.ReloadCh != nil {
			select {
			case c.server.ReloadCh <- struct{}{}:
			default:
			}
		} else {
			log.WithField("tag", c.tag).Error("Reload channel is nil")
		}
		// Apply config change through central reload path; avoid mutating users
		// against a runtime that is about to be replaced.
		return nil
	}
	log.WithField("tag", c.tag).Debug("Node info no change")

	// get user info
	newU, err := c.apiClient.GetUserList()
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get user list failed")
		return nil
	}
	// get user alive
	newA, err := c.apiClient.GetUserAlive()
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get alive list failed")
		return nil
	}

	// update alive list
	if newA != nil {
		c.limiter.SetAliveList(newA)
	}
	// nil means 304 (no change), empty slice means panel returned no users
	if newU == nil {
		log.WithField("tag", c.tag).Debug("User list no change")
		return nil
	}
	deleted, added := compareUserList(c.userList, newU)
	if len(deleted) > 0 {
		// have deleted users
		err = c.server.DelUsers(deleted, c.tag, c.info)
		if err != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Error("Delete users failed")
			return nil
		}
	}
	if len(added) > 0 {
		// have added users
		_, err = c.server.AddUsers(&vCore.AddUsersParams{
			Tag:      c.tag,
			NodeInfo: c.info,
			Users:    added,
		})
		if err != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Error("Add users failed")
			return nil
		}
	}
	if len(added) > 0 || len(deleted) > 0 {
		// update Limiter
		c.limiter.UpdateUser(c.tag, added, deleted)
		if err != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Error("limiter users failed")
			return nil
		}
	}
	c.userList = newU
	if len(added)+len(deleted) != 0 {
		log.WithField("tag", c.tag).
			Infof("%d user deleted, %d user added", len(deleted), len(added))
	}
	return nil
}
