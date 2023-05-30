package main

/*
#include "../c/constants.h"
*/
import "C"

import (
	"fmt"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"net/http"
	"time"
)

type CachedMessage struct {
	id        types.MessageID
	text      string
	timestamp time.Time
}

/*
 * Holds all data for one connection.
 */
type Handler struct {
	account          *PurpleAccount
	username         string
	log              waLog.Logger
	container        *sqlstore.Container
	client           *whatsmeow.Client
	deferredReceipts map[types.JID]map[types.JID][]types.MessageID // holds ID and sender of a received message so the receipt can be sent later.
	cachedMessages   []CachedMessage                               // for looking up reactions and quotes
	pictureRequests  chan ProfilePictureRequest
	httpClient       *http.Client // for executing picture requests
}

/*
 * This plug-in can handle multiple connections (identified by user-supplied name).
 */
var handlers = make(map[*PurpleAccount]*Handler)

/*
 * Handle incoming events.
 *
 * Largely based on https://github.com/tulir/whatsmeow/blob/main/mdtest/main.go.
 */
func (handler *Handler) eventHandler(rawEvt interface{}) {
	log := handler.log
	cli := handler.client
	switch evt := rawEvt.(type) {
	case *events.AppStateSyncComplete:
		// this happens after initial logon via QR code (after Connected, but before HistorySync event)
		if evt.Name == appstate.WAPatchCriticalBlock {
			log.Infof("AppStateSyncComplete and WAPatchCriticalBlock")
			handler.handle_connected()
		}
	case *events.PushNameSetting:
		log.Infof("%#v", evt)
		// Send presence when the pushname is changed remotely.
		// This makes sure that outgoing messages always have the right pushname.
		// This is making a round-trip through purple so user can decide to
		// be "away" instead of "online"
		handler.handle_connected()
	case *events.PushName:
		log.Infof("%#v", evt)
		// other device changed our friendly name
		// setting is regarded by whatsmeow internally
		// no need to forward to purple
		// TODO: find out how this is related to the PushNameSetting event
	case *events.Connected:
		// connected – start downloading profile pictures now.
		go handler.profile_picture_downloader()
		handler.handle_connected()
	case *events.Disconnected:
		// TODO: Find out if it would be more sensible to handle this as a non-error disconnect.
		purple_error(handler.account, "Disconnected.", ERROR_TRANSIENT)
	case *events.StreamReplaced:
		// TODO: find out when exactly this happens and how to handle it (fatal or transient error)
		// working theory: when more than four devices are connected, WhatsApp servers drop the oldest connection
		// NOTE: evt contains no data
		purple_error(handler.account, "Connection stream has been replaced.", ERROR_TRANSIENT)
	case *events.Message:
		handler.handle_message(evt.Message, evt.Info.ID, evt.Info.MessageSource, &evt.Info.PushName, evt.Info.Timestamp, false)
	case *events.Receipt:
		if evt.Type == events.ReceiptTypeRead || evt.Type == events.ReceiptTypeReadSelf {
			log.Infof("%v was read by %s at %s", evt.MessageIDs, evt.SourceString(), evt.Timestamp)
		} else if evt.Type == events.ReceiptTypeDelivered {
			log.Infof("%s was delivered to %s at %s", evt.MessageIDs[0], evt.SourceString(), evt.Timestamp)
		}
	case *events.Presence:
		handler.handle_presence(evt)
	case *events.HistorySync:
		// this happens after initial logon via QR code (after AppStateSyncComplete)
		pushnames := evt.Data.GetPushnames()
		for _, p := range pushnames {
			if p.Id != nil && p.Pushname != nil {
				purple_update_name(handler.account, *p.Id, *p.Pushname)
			}
		}
		if purple_get_bool(handler.account, C.GOWHATSAPP_FETCH_HISTORY_OPTION, false) {
			handler.handle_historical_conversations(evt.Data.GetConversations())
		}
	case *events.ChatPresence:
		handler.handle_chat_presence(evt)
	case *events.AppState:
		log.Debugf("App state event: %+v / %+v", evt.Index, evt.SyncActionValue)
	case *events.LoggedOut:
		purple_error(handler.account, "Logged out. Please link again.", ERROR_FATAL)
	case *events.QR:
		handler.handle_qrcode(evt.Codes)
	case *events.PairSuccess:
		log.Infof("PairSuccess: %#v", evt)
		log.Infof("client.Store: %#v", cli.Store)
		if cli.Store.ID == nil {
			purple_error(handler.account, "Pairing succeded, but device ID is missing.", ERROR_FATAL)
		} else if evt.ID.ToNonAD().String() != handler.username {
			purple_error(handler.account, fmt.Sprintf("Your username '%s' does not match the main device's ID '%s'. Please adjust your username.", handler.username, evt.ID.ToNonAD().String()), ERROR_FATAL)
		} else {
			set_credentials(handler.account, *cli.Store.ID, cli.Store.RegistrationID)
			purple_pairing_succeeded(handler.account)
			handler.prune_devices(*cli.Store.ID)
		}
	case *events.CallOffer:
		bcm := evt.BasicCallMeta
		chat := bcm.From.ToNonAD().String()
		sender := bcm.CallCreator.ToNonAD().String()
		text := "This contact is trying to call you, but WhatsApp Web does not support calls."
		purple_display_text_message(handler.account, chat, false, false, sender, nil, bcm.Timestamp, text)
	case *events.CallOfferNotice:
		// same as CallOffer, but is a group
		bcm := evt.BasicCallMeta
		chat := bcm.From.ToNonAD().String()
		sender := bcm.CallCreator.ToNonAD().String()
		text := "This contact is trying to call you, but WhatsApp Web does not support calls."
		purple_display_text_message(handler.account, chat, true, false, sender, nil, bcm.Timestamp, text)
	case *events.CallRelayLatency:
		// related to calls. ignore silently.
	case *events.CallTerminate:
		// related to calls. ignore silently.
	//case *events.JoinedGroup:
	// TODO
	// received when being added to a group directly
	// NOTE: Spectrum users are not notified if they have been joined to a group. Their XMPP client always needs to join explicitly.
	// &events.JoinedGroup{Reason:"", GroupInfo:types.GroupInfo{JID:types.JID{User:"REDACTED", Agent:0x0, Device:0x0, Server:"g.us", AD:false}, OwnerJID:types.JID{User:"", Agent:0x0, Device:0x0, Server:"", AD:false}, GroupName:types.GroupName{Name:"Testgruppe", NameSetAt:time.Date(2020, time.July, 18, 22, 14, 30, 0, time.Local), NameSetBy:types.JID{User:"", Agent:0x0, Device:0x0, Server:"", AD:false}}, GroupTopic:types.GroupTopic{Topic:"", TopicID:"", TopicSetAt:time.Date(1, time.January, 1, 0, 0, 0, 0, time.UTC), TopicSetBy:types.JID{User:"", Agent:0x0, Device:0x0, Server:"", AD:false}}, GroupLocked:types.GroupLocked{IsLocked:false}, GroupAnnounce:types.GroupAnnounce{IsAnnounce:false, AnnounceVersionID:"REDACTED"}, GroupEphemeral:types.GroupEphemeral{IsEphemeral:false, DisappearingTimer:0x0}, GroupCreated:time.Date(2020, time.July, 18, 22, 14, 30, 0, time.Local), ParticipantVersionID:"REDACTED", Participants:[]types.GroupParticipant{types.GroupParticipant{JID:types.JID{User:"REDACTED", Agent:0x0, Device:0x0, Server:"s.whatsapp.net", AD:false}, IsAdmin:false, IsSuperAdmin:false}, types.GroupParticipant{JID:types.JID{User:"REDACTED", Agent:0x0, Device:0x0, Server:"s.whatsapp.net", AD:false}, IsAdmin:true, IsSuperAdmin:false}}}}
	case *events.OfflineSyncCompleted:
	// TODO
	// no idea what this does
	// &events.OfflineSyncCompleted{Count:0}
	default:
		log.Warnf("Event type not handled: %#v", rawEvt)
	}
}
