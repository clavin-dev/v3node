package node

import (
	log "github.com/sirupsen/logrus"
)

func (c *Controller) isPanelOffline() bool {
	if c == nil || !c.panelOfflineMode {
		return false
	}
	c.panelStateLock.RLock()
	offline := c.panelOffline
	c.panelStateLock.RUnlock()
	return offline
}

func (c *Controller) markPanelFailure(op string, err error) {
	if c == nil || !c.panelOfflineMode {
		return
	}

	c.panelStateLock.Lock()
	c.panelFailCount++
	threshold := c.panelFailThreshold
	if threshold <= 0 {
		threshold = 3
		c.panelFailThreshold = threshold
	}
	wasOffline := c.panelOffline
	if c.panelFailCount >= threshold {
		c.panelOffline = true
	}
	nowOffline := c.panelOffline
	failCount := c.panelFailCount
	c.panelStateLock.Unlock()

	if nowOffline && !wasOffline {
		log.WithFields(log.Fields{
			"tag":       c.tag,
			"op":        op,
			"fails":     failCount,
			"threshold": threshold,
			"err":       err,
		}).Warn("Panel disconnected, entering offline mode")
	}
}

func (c *Controller) markPanelSuccess(op string) {
	if c == nil || !c.panelOfflineMode {
		return
	}

	c.panelStateLock.Lock()
	wasOffline := c.panelOffline
	c.panelOffline = false
	c.panelFailCount = 0
	c.panelStateLock.Unlock()

	if wasOffline {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"op":  op,
		}).Info("Panel recovered, leaving offline mode")
	}
}
