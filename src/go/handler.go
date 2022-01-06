package main

import (
	"fmt"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"mime"
)

/*
 * Holds all data for one connection.
 */
type Handler struct {
	client   *whatsmeow.Client
	username string
	log      waLog.Logger
}

/*
 * This plug-in can handle multiple connections (identified by user-supplied name).
 */
var handlers = make(map[string]*Handler)

func (handler *Handler) eventHandler(rawEvt interface{}) {
	log := handler.log
	cli := handler.client
	switch evt := rawEvt.(type) {
	case *events.AppStateSyncComplete:
		// TODO: find out what this does
		if len(cli.Store.PushName) > 0 && evt.Name == appstate.WAPatchCriticalBlock {
			err := cli.SendPresence(types.PresenceAvailable)
			if err != nil {
				log.Warnf("AppStateSyncComplete: Failed to send available presence: %v", err)
			} else {
				log.Infof("AppStateSyncComplete: Marked self as available")
			}
		}
	case *events.Connected, *events.PushNameSetting:
		if len(cli.Store.PushName) == 0 {
			return
		}
		// Send presence available when connecting and when the pushname is changed.
		// This makes sure that outgoing messages always have the right pushname.
		err := cli.SendPresence(types.PresenceAvailable)
		if err != nil {
			log.Warnf("Failed to send available presence: %v", err)
		} else {
			log.Infof("Marked self as available")
		}
		purple_connected(handler.username)
	case *events.StreamReplaced:
		log.Errorf("StreamReplaced: %+v", evt)
		//os.Exit(0)
		// TODO: signal error
	case *events.Message:
		log.Infof("Received message: %#v", evt)
		text := evt.Message.GetConversation()
		quote := ""
		etm := evt.Message.ExtendedTextMessage
		if etm != nil {
			quote = etm.ContextInfo.QuotedMessage.GetConversation()
		}
		purple_display_text_message(handler.username, evt.Info.ID, evt.Info.MessageSource.Chat.ToNonAD().String(), evt.Info.MessageSource.IsGroup, evt.Info.MessageSource.IsFromMe, evt.Info.MessageSource.Sender.ToNonAD().String(), evt.Info.PushName, evt.Info.Timestamp, text, quote)
		// TODO: investigate evt.Info.SourceString() in context of group messages

		// TODO: implement receiving files
		img := evt.Message.GetImageMessage()
		if img != nil {
			// data, err := cli.Download(img)
			exts, _ := mime.ExtensionsByType(img.GetMimetype())
			path := fmt.Sprintf("%s%s", evt.Info.ID, exts[0])
			log.Infof("ImageMessage: %s", path)
		}
	case *events.Receipt:
		if evt.Type == events.ReceiptTypeRead || evt.Type == events.ReceiptTypeReadSelf {
			log.Infof("%v was read by %s at %s", evt.MessageIDs, evt.SourceString(), evt.Timestamp)
		} else if evt.Type == events.ReceiptTypeDelivered {
			log.Infof("%s was delivered to %s at %s", evt.MessageIDs[0], evt.SourceString(), evt.Timestamp)
		}
	case *events.Presence:
		if evt.Unavailable {
			if evt.LastSeen.IsZero() {
				log.Infof("%s is now offline", evt.From)
			} else {
				log.Infof("%s is now offline (last seen: %s)", evt.From, evt.LastSeen)
			}
		} else {
			log.Infof("%s is now online", evt.From)
		}
	case *events.HistorySync:
		log.Infof("history sync: %+v", evt.Data)
	case *events.ChatPresence:
		who := evt.MessageSource.Chat.ToNonAD().String()
		switch evt.State {
		case types.ChatPresenceComposing:
			purple_composing(handler.username, who)
		case types.ChatPresencePaused:
			purple_paused(handler.username, who)
		}
	case *events.AppState:
		log.Debugf("App state event: %+v / %+v", evt.Index, evt.SyncActionValue)
	default:
		log.Warnf("Event type not handled: %#v", rawEvt)
	}
}
