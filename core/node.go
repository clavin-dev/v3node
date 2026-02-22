package core

import (
	"fmt"
	"strings"

	panel "github.com/clavin-dev/v3node/api/v2board"
	"github.com/clavin-dev/v3node/common/counter"
	"github.com/clavin-dev/v3node/core/app/dispatcher"
)

func (v *V2Core) AddNode(tag string, info *panel.NodeInfo) error {
	inBoundConfig, err := buildInbound(info, tag)
	if err != nil {
		return fmt.Errorf("build inbound error: %s", err)
	}
	err = v.addInbound(inBoundConfig)
	if err != nil {
		return fmt.Errorf("add inbound error: %s", err)
	}
	return nil
}

func (v *V2Core) DelNode(tag string) error {
	v.cleanupNodeRuntime(tag)
	err := v.removeInbound(tag)
	if err != nil {
		return fmt.Errorf("remove in error: %s", err)
	}
	return nil
}

func (v *V2Core) cleanupNodeRuntime(tag string) {
	if tag == "" {
		return
	}

	prefix := tag + "|"

	// Clean cached UID map entries for this node.
	if v.users != nil {
		v.users.mapLock.Lock()
		for email := range v.users.uidMap {
			if strings.HasPrefix(email, prefix) {
				delete(v.users.uidMap, email)
			}
		}
		v.users.mapLock.Unlock()
	}

	if v.dispatcher == nil {
		return
	}

	// Clean traffic counters for this node tag.
	if tcRaw, ok := v.dispatcher.Counter.Load(tag); ok {
		if tc, ok := tcRaw.(*counter.TrafficCounter); ok {
			tc.Counters.Range(func(key, _ interface{}) bool {
				if email, ok := key.(string); ok {
					tc.Delete(email)
				}
				return true
			})
		}
		v.dispatcher.Counter.Delete(tag)
	}

	// Close and remove link managers belonging to this node.
	v.dispatcher.LinkManagers.Range(func(key, value interface{}) bool {
		email, ok := key.(string)
		if !ok || !strings.HasPrefix(email, prefix) {
			return true
		}
		if lm, ok := value.(*dispatcher.LinkManager); ok {
			lm.CloseAll()
		}
		v.dispatcher.LinkManagers.Delete(key)
		return true
	})
}
