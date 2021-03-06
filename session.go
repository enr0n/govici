// Copyright (C) 2019 Nick Rosbrook
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

// Package vici implements a strongSwan vici protocol client. The Go package is
// documented here. For a complete overview and specification of the vici
// protocol visit:
//
//     https://www.strongswan.org/apidoc/md_src_libcharon_plugins_vici_README.html
//
package vici

import (
	"sync"
)

// Session is a vici client session.
type Session struct {
	// Only one command can be active on the transport at a time,
	// but events may get raised at any time while registered, even
	// during an active command request command. So, give session two
	// transports: one is locked with mutex during use, e.g. command
	// requests (including streamed requests), and the other is used
	// for listening to registered events.
	mux sync.Mutex
	ctr *transport

	el *eventListener
}

// NewSession returns a new vici session.
func NewSession() (*Session, error) {
	ctr, err := newTransport(nil)
	if err != nil {
		return nil, err
	}
	elt, err := newTransport(nil)
	if err != nil {
		return nil, err
	}

	s := &Session{
		ctr: ctr,
		el:  newEventListener(elt),
	}

	return s, nil
}

// CommandRequest sends a command request to the server, and returns the server's response.
// The command is specified by cmd, and its arguments are provided by msg. An error is returned
// if an error occurs while communicating with the daemon. To determine if a command was successful,
// use Message.CheckError.
func (s *Session) CommandRequest(cmd string, msg *Message) (*Message, error) {
	return s.sendRequest(cmd, msg)
}

// StreamedCommandRequest sends a streamed command request to the server. StreamedCommandRequest
// behaves like CommandRequest, but accepts an event argument, which specifies the event type
// to stream while the command request is active. The complete stream of messages received from
// the server is returned once the request is complete.
func (s *Session) StreamedCommandRequest(cmd string, event string, msg *Message) (*MessageStream, error) {
	return s.sendStreamedRequest(cmd, event, msg)
}

// Listen registers the session to listen for all events given. Listen does not return
// unless the event channel is closed. To receive events that are registered here, use
// NextEvent. Listen should not be called again until the previous call has returned.
func (s *Session) Listen(events []string) error {
	return s.el.safeListen(events)
}

// NextEvent returns the next event received by the session event listener.  NextEvent is a
// blocking call. If there is no event in the event buffer, NextEvent will wait to return until
// a new event is received. An error is returned if the event channel is closed.
func (s *Session) NextEvent() (*Message, error) {
	return s.el.nextEvent()
}
