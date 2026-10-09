package mqtt

import (
	"log/slog"
	"strconv"
	"strings"
)

// Actions are invoked synchronously from the broker's delivery goroutine and must
// return quickly.
type Actions struct {
	LevelCommand        func(nodeID string, db float64)
	SwitchCommand       func(nodeID, parameter string, on bool)
	RecallParameterSet  func(id int)
	HomeAssistantOnline func()
}

// Subscriptions are the topics the bridge listens on.
var Subscriptions = []string{BlockLevelSetWildcard, BlockEnabledSetWildcard, BlockPhantomSetWildcard, ParameterSetWildcard, HAStatusTopic}

// ParseOnOffPayload accepts ON/OFF (case-insensitive) and true/false.
func ParseOnOffPayload(payload string) (bool, bool) {
	switch strings.ToUpper(strings.TrimSpace(payload)) {
	case PayloadOn, "TRUE", "1":
		return true, true
	case PayloadOff, "FALSE", "0":
		return false, true
	}
	return false, false
}

// NewRouter maps inbound topics to Actions.
func NewRouter(actions Actions, log *slog.Logger) MessageHandler {
	return func(topic string, payload []byte) {
		text := strings.TrimSpace(string(payload))
		if cmd, ok := ParseBlockCommand(topic); ok {
			if cmd.Parameter == ParameterLevel {
				db, err := strconv.ParseFloat(text, 64)
				if err != nil {
					log.Warn("level_command_invalid", "node_id", cmd.NodeID, "payload", text)
					return
				}
				actions.LevelCommand(cmd.NodeID, db)
				return
			}
			on, ok := ParseOnOffPayload(text)
			if !ok {
				log.Warn("switch_command_invalid", "node_id", cmd.NodeID, "parameter", cmd.Parameter, "payload", text)
				return
			}
			actions.SwitchCommand(cmd.NodeID, cmd.Parameter, on)
			return
		}
		if id, ok := ParseParameterSetCommand(topic); ok {
			actions.RecallParameterSet(id)
			return
		}
		if topic == HAStatusTopic {
			if text == PayloadOnline {
				log.Info("home_assistant_online")
				actions.HomeAssistantOnline()
			}
			return
		}
		log.Debug("mqtt_message_ignored", "topic", topic)
	}
}
