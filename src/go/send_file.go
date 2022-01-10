package main

import (
	"context"
	"fmt"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
	"net/http"
	"os"
	"path"
)

// based on https://github.com/tulir/whatsmeow/blob/main/mdtest/main.go
func (handler *Handler) send_file(who string, filename string) int {
	recipient, err := parseJID(who)
	if err != nil {
		purple_error(handler.account, fmt.Sprintf("%#v", err))
		return 14 // EFAULT "Bad address"
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		handler.log.Errorf("Failed to open %s: %v", filename, err)
		return 5 //EIO "Input/output error"
	}
	var msg *waProto.Message
	mimetype := http.DetectContentType(data)
	if mimetype == "image/jpeg" {
		msg, err = handler.send_file_image(data, mimetype)
	} else {
		basename := path.Base(filename) // TODO: find out whether this should be with or without extension. WhatsApp server seems to add extention.
		msg, err = handler.send_file_document(data, mimetype, basename)
	}
	if err != nil {
		handler.log.Errorf("Failed to upload file: %v", err)
		return 32 //EPIPE
	}
	ts, err := handler.client.SendMessage(recipient, "", msg)
	if err != nil {
		handler.log.Errorf("Error sending file: %v", err)
		return 70 //ECOMM
	} else {
		handler.log.Infof("Attachment sent (server timestamp: %s)", ts)
		return 0
	}
}

func (handler *Handler) send_file_image(data []byte, mimetype string) (*waProto.Message, error) {
	uploaded, err := handler.client.Upload(context.Background(), data, whatsmeow.MediaImage)
	if err != nil {
		return nil, err
	}
	msg := &waProto.Message{ImageMessage: &waProto.ImageMessage{
		Url:           proto.String(uploaded.URL),
		DirectPath:    proto.String(uploaded.DirectPath),
		MediaKey:      uploaded.MediaKey,
		Mimetype:      proto.String(mimetype),
		FileEncSha256: uploaded.FileEncSHA256,
		FileSha256:    uploaded.FileSHA256,
		FileLength:    proto.Uint64(uint64(len(data))),
	}}
	return msg, nil
}

func (handler *Handler) send_file_document(data []byte, mimetype string, filename string) (*waProto.Message, error) {
	uploaded, err := handler.client.Upload(context.Background(), data, whatsmeow.MediaDocument)
	if err != nil {
		return nil, err
	}
	msg := &waProto.Message{DocumentMessage: &waProto.DocumentMessage{
		Title:         proto.String(filename),
		Url:           proto.String(uploaded.URL),
		DirectPath:    proto.String(uploaded.DirectPath),
		MediaKey:      uploaded.MediaKey,
		Mimetype:      proto.String(mimetype),
		FileEncSha256: uploaded.FileEncSHA256,
		FileSha256:    uploaded.FileSHA256,
		FileLength:    proto.Uint64(uint64(len(data))),
	}}
	return msg, nil
}
