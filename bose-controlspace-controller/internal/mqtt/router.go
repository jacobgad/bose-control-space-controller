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
	MuteCommand         func(nodeID string, muted bool)
	RecallParameterSet  func(id int)
	HomeAssistantOnline func()
}

// Subscriptions are the topics the bridge listens on.
var Subscriptions = []string{BlockLevelSetWildcard, BlockMuteSetWildcard, ParameterSetWildcard, HAStatusTopic}

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
			switch cmd.Parameter {
			case "level":
				db, err := strconv.ParseFloat(text, 64)
				if err != nil {
					log.Warn("level_command_invalid", "node_id", cmd.NodeID, "payload", text)
					return
				}
				actions.LevelCommand(cmd.NodeID, db)
			case "mute":
				muted, ok := ParseOnOffPayload(text)
				if !ok {
					log.Warn("mute_command_invalid", "node_id", cmd.NodeID, "payload", text)
					return
				}
				actions.MuteCommand(cmd.NodeID, muted)
			}
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
