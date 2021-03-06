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

package vici

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"reflect"
)

const (
	// Begin a new section having a name
	msgSectionStart uint8 = iota + 1

	// End a previously started section
	msgSectionEnd

	// Define a value for a named key in the current section
	msgKeyValue

	// Begin a name list for list items
	msgListStart

	// Dfeine an unnamed item value in the current list
	msgListItem

	// End a prevsiously started list
	msgListEnd
)

var (
	// Generic encoding/decoding and marshaling/unmarshaling errors
	errEncoding  = errors.New("vici: error encoding message")
	errDecoding  = errors.New("vici: error decoding message")
	errMarshal   = errors.New("vici: error marshaling message")
	errUnmarshal = errors.New("vici: error unmarshaling message")

	// Encountered unsupported type when encoding a message
	errUnsupportedType = errors.New("vici: unsupported message element type")

	// Used in CheckError - the 'success' field was set to "no"
	errCommandFailed = errors.New("vici: command failed")

	// Base message for decoding errors that are due to an incorrectly formatted message
	errMalformedMessage = errors.New("vici: malformed message")

	// Malformed message errors
	errBadKey            = fmt.Errorf("%v: expected key length does not match actual length", errMalformedMessage)
	errBadValue          = fmt.Errorf("%v: expected value length does not match actual length", errMalformedMessage)
	errEndOfBuffer       = fmt.Errorf("%v: unexpected end of buffer", errMalformedMessage)
	errExpectedBeginning = fmt.Errorf("%v: expected beginning of message element", errMalformedMessage)

	// Marshaling errors
	errMarshalUnsupportedType = fmt.Errorf("%v: encountered unsupported type", errMarshal)

	// Unmarshaling errors
	errUnmarshalBadType      = fmt.Errorf("%v: type must be non-nil pointer", errUnmarshal)
	errUnmarshalTypeMismatch = fmt.Errorf("%v: incompatible types", errUnmarshal)
	errUnmarshalNonMessage   = fmt.Errorf("%v: encountered non-message type", errUnmarshal)
)

// MessageStream is used to feed continuous data during a command request, and simply
// contains a slice of *Message.
type MessageStream struct {
	// Message list
	messages []*Message
}

// Messages returns the messages received from the streamed request.
func (ms *MessageStream) Messages() []*Message {
	return ms.messages
}

// Message represents a vici message.
//
// A Message ensures that elements are encoded in the order they are
// added to the message, through the usage of Set. Valid message elements
// are key-value pairs, lists, and sections which correspond to the Go types
// string, []string, and *Message respectively.
type Message struct {
	keys []string

	data map[string]interface{}
}

// NewMessage returns an empty Message.
func NewMessage() *Message {
	return &Message{
		keys: make([]string, 0),
		data: make(map[string]interface{}),
	}
}

// MarshalMessage returns a Message marshaled from v. Only exported fields
// with a `vici` tag explicitly set are marshaled. An error is returned
// if v is not a struct (or a pointer to one), or an unsupported Message
// element type is encountered.
func MarshalMessage(v interface{}) (*Message, error) {
	m := NewMessage()
	if err := m.marshal(v); err != nil {
		return nil, err
	}

	return m, nil
}

// UnmarshalMessage unmarshals m to v. Fields of v are ignored unless
// explicitly tagged and exported. The underlying value of v should be
// a pointer to a struct.
func UnmarshalMessage(m *Message, v interface{}) error {
	return m.unmarshal(v)
}

// Set sets key to value. An error is returned if value's underlying
// type is not supported as a Message element type.
//
// If the key already exists the value is overwritten, but the ordering
// of the message is not changed.
func (m *Message) Set(key string, value interface{}) error {
	return m.addItem(key, value)
}

// Get returns the message field identified by key, if it exists. If the
// field does not exist, nil is returned.
func (m *Message) Get(key string) interface{} {
	v, ok := m.data[key]
	if !ok {
		return nil
	}

	return v
}

// Keys returns the list of valid message keys.
func (m *Message) Keys() []string {
	return m.keys
}

// Err examines a command response Message, and determines if it was successful.
// If it was, or if the message does not contain a 'success' field, nil is returned. Otherwise,
// an error is returned using the 'errmsg' field.
func (m *Message) Err() error {
	if success, ok := m.data["success"]; ok {
		if success != "yes" {
			return fmt.Errorf("%v: %v", errCommandFailed, m.data["errmsg"])
		}
	}

	return nil
}

func (m *Message) addItem(key string, value interface{}) error {
	rv := reflect.ValueOf(value)

	// Check if the key is already set in the message
	_, exists := m.data[key]

	switch rv.Kind() {

	case reflect.String:
		m.data[key] = value.(string)

	case reflect.Slice, reflect.Array:
		list, ok := value.([]string)
		if !ok {
			return errUnsupportedType
		}
		m.data[key] = list

	case reflect.Ptr:
		msg, ok := value.(*Message)
		if !ok {
			return errUnsupportedType
		}
		m.data[key] = msg

	default:
		return errUnsupportedType
	}

	// Only append to keys if this is a new key.
	if !exists {
		m.keys = append(m.keys, key)
	}

	return nil
}

type messageElement struct {
	k string
	v interface{}
}

func (m *Message) elements() chan messageElement {
	c := make(chan messageElement)
	go m.orderedIterate(c)

	return c
}

func (m *Message) orderedIterate(c chan messageElement) {
	defer close(c)

	for _, k := range m.keys {
		c <- messageElement{k, m.data[k]}
	}
}

func (m *Message) encode() ([]byte, error) {
	buf := bytes.NewBuffer([]byte{})

	for e := range m.elements() {
		k := e.k
		v := e.v

		rv := reflect.ValueOf(v)

		var (
			data []byte
			err  error
		)

		switch rv.Kind() {

		case reflect.String:
			uv := v.(string)

			data, err = m.encodeKeyValue(k, uv)
			if err != nil {
				return []byte{}, err
			}

		case reflect.Slice, reflect.Array:
			uv := v.([]string)

			data, err = m.encodeList(k, uv)
			if err != nil {
				return []byte{}, err
			}

		case reflect.Ptr:
			uv, ok := v.(*Message)
			if !ok {
				return []byte{}, errUnsupportedType
			}

			data, err = m.encodeSection(k, uv)
			if err != nil {
				return []byte{}, err
			}

		default:
			return []byte{}, errUnsupportedType
		}

		_, err = buf.Write(data)
		if err != nil {
			return []byte{}, fmt.Errorf("%v: %v", errEncoding, err)
		}
	}

	return buf.Bytes(), nil
}

func (m *Message) decode(data []byte) error {
	buf := bytes.NewBuffer(data)

	b, err := buf.ReadByte()
	if err != nil && err != io.EOF {
		return fmt.Errorf("%v: %v", errDecoding, err)
	}

	for buf.Len() > 0 {
		// Determine the next message element
		switch b {

		case msgKeyValue:
			n, err := m.decodeKeyValue(buf.Bytes())
			if err != nil {
				return err
			}
			buf.Next(n)

		case msgListStart:
			n, err := m.decodeList(buf.Bytes())
			if err != nil {
				return err
			}
			buf.Next(n)

		case msgSectionStart:
			n, err := m.decodeSection(buf.Bytes())
			if err != nil {
				return err
			}
			buf.Next(n)
		}

		b, err = buf.ReadByte()
		if err != nil && err != io.EOF {
			return fmt.Errorf("%v: %v", errDecoding, err)
		}
	}

	return nil
}

// encodeKeyValue will return a byte slice of an encoded key-value pair.
//
// The size of the byte slice is the length of the key and value, plus four bytes:
// one byte for message element type, one byte for key length, and two bytes for value
// length.
func (m *Message) encodeKeyValue(key, value string) ([]byte, error) {
	// Initialize buffer to indictate the message element type
	// is a key-value pair
	buf := bytes.NewBuffer([]byte{msgKeyValue})

	// Write the key length and key
	err := buf.WriteByte(uint8(len(key)))
	if err != nil {
		return []byte{}, fmt.Errorf("%v: %v", errEncoding, err)
	}

	_, err = buf.WriteString(key)
	if err != nil {
		return []byte{}, fmt.Errorf("%v: %v", errEncoding, err)
	}

	// Write the value's length to the buffer as two bytes
	vl := make([]byte, 2)
	binary.BigEndian.PutUint16(vl, uint16(len(value)))

	_, err = buf.Write(vl)
	if err != nil {
		return []byte{}, fmt.Errorf("%v: %v", errEncoding, err)
	}

	// Write the value to the buffer
	_, err = buf.WriteString(value)
	if err != nil {
		return []byte{}, fmt.Errorf("%v: %v", errEncoding, err)
	}

	return buf.Bytes(), nil
}

// encodeList will return a byte slice of an encoded list.
//
// The size of the byte slice is the length of the key and total length of
// the list (sum of length of the items in the list), plus three bytes for each
// list item: one for message element type, and two for item length. Another three
// bytes are used to indicate list start and list stop, and the length of the key.
func (m *Message) encodeList(key string, list []string) ([]byte, error) {
	// Initialize buffer to indictate the message element type
	// is the start of a list
	buf := bytes.NewBuffer([]byte{msgListStart})

	// Write the key length and key
	err := buf.WriteByte(uint8(len(key)))
	if err != nil {
		return []byte{}, fmt.Errorf("%v: %v", errEncoding, err)
	}

	_, err = buf.WriteString(key)
	if err != nil {
		return []byte{}, fmt.Errorf("%v: %v", errEncoding, err)
	}

	for _, item := range list {
		// Indicate that this is a list item
		err = buf.WriteByte(msgListItem)
		if err != nil {
			return []byte{}, fmt.Errorf("%v: %v", errEncoding, err)
		}

		// Write the item's length to the buffer as two bytes
		il := make([]byte, 2)
		binary.BigEndian.PutUint16(il, uint16(len(item)))

		_, err = buf.Write(il)
		if err != nil {
			return []byte{}, fmt.Errorf("%v: %v", errEncoding, err)
		}

		// Write the item to the buffer
		_, err = buf.WriteString(item)
		if err != nil {
			return []byte{}, fmt.Errorf("%v: %v", errEncoding, err)
		}
	}

	// Indicate the end of the list
	err = buf.WriteByte(msgListEnd)
	if err != nil {
		return []byte{}, fmt.Errorf("%v: %v", errEncoding, err)
	}

	return buf.Bytes(), nil
}

// encodeSection will return a byte slice of an encoded section
func (m *Message) encodeSection(key string, section *Message) ([]byte, error) {
	// Initialize buffer to indictate the message element type
	// is the start of a section
	buf := bytes.NewBuffer([]byte{msgSectionStart})

	// Write the key length and key
	err := buf.WriteByte(uint8(len(key)))
	if err != nil {
		return []byte{}, fmt.Errorf("%v: %v", errEncoding, err)
	}

	_, err = buf.WriteString(key)
	if err != nil {
		return []byte{}, fmt.Errorf("%v: %v", errEncoding, err)
	}

	// Encode the sections elements
	for e := range section.elements() {
		k := e.k
		v := e.v

		rv := reflect.ValueOf(v)

		var data []byte

		switch rv.Kind() {

		case reflect.String:
			uv := v.(string)

			data, err = m.encodeKeyValue(k, uv)
			if err != nil {
				return []byte{}, err
			}

		case reflect.Slice, reflect.Array:
			uv := v.([]string)

			data, err = m.encodeList(k, uv)
			if err != nil {
				return []byte{}, err
			}

		case reflect.Ptr:
			uv, ok := v.(*Message)
			if !ok {
				return []byte{}, errUnsupportedType
			}

			data, err = m.encodeSection(k, uv)
			if err != nil {
				return []byte{}, err
			}

		default:
			return []byte{}, errUnsupportedType
		}

		_, err = buf.Write(data)
		if err != nil {
			return []byte{}, fmt.Errorf("%v: %v", errEncoding, err)
		}

	}

	// Indicate the end of the section
	err = buf.WriteByte(msgSectionEnd)
	if err != nil {
		return []byte{}, fmt.Errorf("%v: %v", errEncoding, err)
	}

	return buf.Bytes(), nil
}

// decodeKeyValue will decode a key-value pair and write it to the message's
// data, and returns the number of bytes decoded.
func (m *Message) decodeKeyValue(data []byte) (int, error) {
	buf := bytes.NewBuffer(data)

	// Read the key from the buffer
	n, err := buf.ReadByte()
	if err != nil {
		return -1, fmt.Errorf("%v: %v", errDecoding, err)
	}

	keyLen := int(n)
	key := string(buf.Next(keyLen))
	if len(key) != keyLen {
		return -1, errBadKey
	}

	// Read the value's length
	v := buf.Next(2)
	if len(v) != 2 {
		return -1, errEndOfBuffer

	}

	// Read the value from the buffer
	valueLen := int(binary.BigEndian.Uint16(v))
	value := string(buf.Next(valueLen))
	if len(value) != valueLen {
		return -1, errBadValue
	}

	err = m.addItem(key, value)
	if err != nil {
		return -1, fmt.Errorf("%v: %v", errDecoding, err)
	}

	// Return the length of the key and value, plus the three bytes for their
	// lengths
	return keyLen + valueLen + 3, nil
}

// decodeList will decode a list and write it to the message's data, and return
// the number of bytes decoded.
func (m *Message) decodeList(data []byte) (int, error) {
	var list []string

	buf := bytes.NewBuffer(data)

	// Read the key from the buffer
	n, err := buf.ReadByte()
	if err != nil {
		return -1, fmt.Errorf("%v: %v", errDecoding, err)
	}

	keyLen := int(n)
	key := string(buf.Next(keyLen))
	if len(key) != keyLen {
		return -1, errBadKey
	}

	b, err := buf.ReadByte()
	if err != nil {
		return -1, fmt.Errorf("%v: %v", errDecoding, err)
	}

	// Keep track of bytes decoded
	count := keyLen + 2

	// Read the list from the buffer
	for b != msgListEnd {
		// Ensure this is the beginning of a list item
		if b != msgListItem {
			return -1, errExpectedBeginning
		}

		// Read the value's length
		v := buf.Next(2)
		if len(v) != 2 {
			return -1, errEndOfBuffer

		}

		// Read the value from the buffer
		valueLen := int(binary.BigEndian.Uint16(v))
		value := string(buf.Next(valueLen))
		if len(value) != valueLen {
			return -1, errBadValue
		}

		list = append(list, value)

		b, err = buf.ReadByte()
		if err != nil {
			return -1, fmt.Errorf("%v: %v", errDecoding, err)
		}

		count += valueLen + 3
	}

	err = m.addItem(key, list)
	if err != nil {
		return -1, fmt.Errorf("%v: %v", errDecoding, err)
	}

	return count, nil
}

// decodeSection will decode a section into a message's data, and return the number
// of bytes decoded.
func (m *Message) decodeSection(data []byte) (int, error) {
	section := NewMessage()

	buf := bytes.NewBuffer(data)

	// Read the key from the buffer
	n, err := buf.ReadByte()
	if err != nil {
		return -1, fmt.Errorf("%v: %v", errDecoding, err)
	}

	keyLen := int(n)
	key := string(buf.Next(keyLen))
	if len(key) != keyLen {
		return -1, errBadKey
	}

	b, err := buf.ReadByte()
	if err != nil {
		return -1, fmt.Errorf("%v: %v", errDecoding, err)
	}

	// Keep track of bytes decoded
	count := keyLen + 2

	for b != msgSectionEnd {
		// Determine the next message element
		switch b {

		case msgKeyValue:
			n, err := section.decodeKeyValue(buf.Bytes())
			if err != nil {
				return -1, err
			}
			// Skip those decoded bytes
			buf.Next(n)

			count += n

		case msgListStart:
			n, err := section.decodeList(buf.Bytes())
			if err != nil {
				return -1, err
			}
			// Skip those decoded bytes
			buf.Next(n)

			count += n

		case msgSectionStart:
			n, err := section.decodeSection(buf.Bytes())
			if err != nil {
				return -1, err
			}
			// Skip those decoded bytes
			buf.Next(n)

			count += n

		default:
			return -1, errExpectedBeginning
		}

		b, err = buf.ReadByte()
		if err != nil {
			return -1, fmt.Errorf("%v: %v", errDecoding, err)
		}

		count++
	}

	err = m.addItem(key, section)
	if err != nil {
		return -1, err
	}

	return count, nil
}

// messageTag is used for parsing struct tags in marshaling Messages
type messageTag struct {
	name string

	skip bool
}

func newMessageTag(tag reflect.StructTag) messageTag {
	t := tag.Get("vici")
	if t == "-" || t == "" {
		return messageTag{skip: true}
	}

	return messageTag{name: t}
}

func emptyMessageElement(rv reflect.Value) bool {
	switch rv.Kind() {

	case reflect.Slice:
		return rv.IsNil()

	case reflect.Struct:
		z := true
		for i := 0; i < rv.NumField(); i++ {
			z = z && emptyMessageElement(rv.Field(i))
		}
		return z
	}

	return rv.Interface() == reflect.Zero(rv.Type()).Interface()
}

func (m *Message) marshal(v interface{}) error {
	rv := reflect.ValueOf(v)

	// v must either be a struct or a pointer to one
	if rv.Kind() == reflect.Ptr {
		rv = reflect.Indirect(rv)
	}

	if rv.Kind() != reflect.Struct {
		return fmt.Errorf("%v: %v", errMarshalUnsupportedType, rv.Kind())
	}

	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		rf := rt.Field(i)

		mt := newMessageTag(rf.Tag)
		if mt.skip {
			continue
		}

		rfv := rv.Field(i)
		if !rfv.CanInterface() {
			continue
		}

		if emptyMessageElement(rfv) {
			continue
		}

		// Add the message element
		err := m.marshalField(mt.name, rfv)
		if err != nil {
			return err
		}
	}

	return nil
}

func (m *Message) marshalField(name string, rv reflect.Value) error {
	switch rv.Kind() {

	case reflect.String, reflect.Slice, reflect.Array:
		return m.addItem(name, rv.Interface())

	case reflect.Ptr:
		if _, ok := rv.Interface().(*Message); ok {
			return m.addItem(name, rv.Interface())
		}

		msg := NewMessage()
		if err := msg.marshal(rv.Interface()); err != nil {
			return err
		}

		return m.addItem(name, msg)

	case reflect.Struct:
		msg := NewMessage()
		if err := msg.marshal(rv.Interface()); err != nil {
			return err
		}

		return m.addItem(name, msg)

	default:
		return fmt.Errorf("%v: %v", errMarshalUnsupportedType, rv.Kind())
	}
}

func (m *Message) unmarshal(v interface{}) error {
	rv := reflect.ValueOf(v)

	if rv.Kind() != reflect.Ptr {
		return errUnmarshalBadType
	}

	if rv.IsNil() {
		return errUnmarshalBadType
	}

	rt := reflect.Indirect(rv).Type()
	for i := 0; i < rt.NumField(); i++ {
		rf := rt.Field(i)
		tag := newMessageTag(rf.Tag)

		value, ok := m.data[tag.name]
		if !ok {
			continue
		}

		rfv := rv.Elem().Field(i)
		if !rfv.CanInterface() {
			continue
		}

		err := m.unmarshalField(rfv, reflect.ValueOf(value))
		if err != nil {
			return err
		}
	}

	return nil
}

func (m *Message) unmarshalField(field reflect.Value, rv reflect.Value) error {
	switch field.Kind() {

	case reflect.String:
		if _, ok := rv.Interface().(string); !ok {
			return fmt.Errorf("%v: string and %v", errUnmarshalTypeMismatch, rv.Type())
		}
		field.Set(rv)

	case reflect.Slice:
		if _, ok := rv.Interface().([]string); !ok {
			return fmt.Errorf("%v: []string and %v", errUnmarshalTypeMismatch, rv.Type())
		}
		field.Set(rv)

	case reflect.Ptr:
		if _, ok := field.Interface().(*Message); ok {
			field.Set(rv)

			return nil
		}

		msg, ok := rv.Interface().(*Message)
		if !ok {
			return fmt.Errorf("%v: %v", errUnmarshalNonMessage, rv.Type())
		}

		return msg.unmarshal(field.Interface())

	case reflect.Struct:
		msg, ok := rv.Interface().(*Message)
		if !ok {
			return fmt.Errorf("%v: %v", errUnmarshalNonMessage, rv.Type())
		}

		fp := reflect.New(field.Type())
		if err := msg.unmarshal(fp.Interface()); err != nil {
			return err
		}

		field.Set(reflect.Indirect(fp))
	}

	return nil
}
