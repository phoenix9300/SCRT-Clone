package vbs

import (
	"reflect"
	"strings"
	"time"
)

// dictKey is the comparable form of a Dictionary key.
type dictKey struct {
	kind byte
	s    string
	n    float64
	o    any
}

// Dictionary is the Scripting.Dictionary object. Keys keep their insertion
// order. Hosts may create dictionaries with NewDictionary and hand them to
// scripts, or inspect dictionaries received from scripts.
type Dictionary struct {
	keys  []Value
	vals  []Value
	index map[dictKey]int
	text  bool // CompareMode = vbTextCompare
}

// NewDictionary creates an empty Scripting.Dictionary.
func NewDictionary() *Dictionary {
	return &Dictionary{index: map[dictKey]int{}}
}

func (d *Dictionary) key(k Value) (dictKey, error) {
	switch x := k.(type) {
	case nil:
		return dictKey{kind: 'e'}, nil
	case nullType:
		return dictKey{kind: 'n'}, nil
	case string:
		if d.text {
			return dictKey{kind: 's', s: strings.ToLower(x)}, nil
		}
		return dictKey{kind: 's', s: x}, nil
	case bool:
		return dictKey{kind: 'b', n: float64(boolNum(x))}, nil
	case int64:
		return dictKey{kind: 'f', n: float64(x)}, nil
	case float64:
		return dictKey{kind: 'f', n: x}, nil
	case time.Time:
		return dictKey{kind: 'd', n: dateToFloat(x)}, nil
	case *Array:
		return dictKey{}, newErr(13, "")
	case Object:
		if t := reflect.TypeOf(k); t == nil || !t.Comparable() {
			return dictKey{}, newErr(13, "")
		}
		return dictKey{kind: 'o', o: k}, nil
	}
	if n := normalize(k); n != k {
		return d.key(n)
	}
	return dictKey{}, newErr(13, "")
}

// Len returns the number of entries.
func (d *Dictionary) Len() int { return len(d.keys) }

// Lookup returns the item stored under key.
func (d *Dictionary) Lookup(key Value) (Value, bool) {
	k, err := d.key(normalize(key))
	if err != nil {
		return nil, false
	}
	i, ok := d.index[k]
	if !ok {
		return nil, false
	}
	return d.vals[i], true
}

// Put stores val under key, replacing an existing entry. It reports false
// when key cannot be used as a dictionary key (arrays).
func (d *Dictionary) Put(key, val Value) bool {
	key = normalize(key)
	k, err := d.key(key)
	if err != nil {
		return false
	}
	d.put(k, key, normalize(val))
	return true
}

func (d *Dictionary) put(k dictKey, key, val Value) {
	val = storeValue(val)
	if i, ok := d.index[k]; ok {
		d.vals[i] = val
		return
	}
	d.index[k] = len(d.keys)
	d.keys = append(d.keys, key)
	d.vals = append(d.vals, val)
}

// Delete removes key and reports whether it was present.
func (d *Dictionary) Delete(key Value) bool {
	k, err := d.key(normalize(key))
	if err != nil {
		return false
	}
	i, ok := d.index[k]
	if !ok {
		return false
	}
	delete(d.index, k)
	d.keys = append(d.keys[:i], d.keys[i+1:]...)
	d.vals = append(d.vals[:i], d.vals[i+1:]...)
	for kk, idx := range d.index {
		if idx > i {
			d.index[kk] = idx - 1
		}
	}
	return true
}

// Keys returns a copy of the keys in insertion order.
func (d *Dictionary) Keys() []Value { return append([]Value{}, d.keys...) }

// Values returns a copy of the items in insertion order.
func (d *Dictionary) Values() []Value { return append([]Value{}, d.vals...) }

// TypeName implements TypeNamer.
func (d *Dictionary) TypeName() string { return "Dictionary" }

// Enumerate implements Enumerable: For Each visits the keys.
func (d *Dictionary) Enumerate() []Value { return d.Keys() }

func (d *Dictionary) gcRefs() []Value {
	return append(append(make([]Value, 0, 2*len(d.keys)), d.keys...), d.vals...)
}

// Get implements Object.
func (d *Dictionary) Get(name string, args []Value) (Value, error) {
	switch name {
	case "add":
		if err := needArgs(args, 2, 2); err != nil {
			return nil, err
		}
		k, err := d.key(args[0])
		if err != nil {
			return nil, err
		}
		if _, dup := d.index[k]; dup {
			return nil, newErr(457, "")
		}
		d.put(k, args[0], args[1])
		return nil, nil
	case "exists":
		if err := needArgs(args, 1, 1); err != nil {
			return nil, err
		}
		k, err := d.key(args[0])
		if err != nil {
			return nil, err
		}
		_, ok := d.index[k]
		return ok, nil
	case "", "item":
		if err := needArgs(args, 1, 1); err != nil {
			return nil, err
		}
		k, err := d.key(args[0])
		if err != nil {
			return nil, err
		}
		if i, ok := d.index[k]; ok {
			return d.vals[i], nil
		}
		// Reading a missing key creates it, as the real object does.
		d.put(k, args[0], nil)
		return nil, nil
	case "items":
		return newTempArray(d.Values()), nil
	case "keys":
		return newTempArray(d.Keys()), nil
	case "remove":
		if err := needArgs(args, 1, 1); err != nil {
			return nil, err
		}
		if _, err := d.key(args[0]); err != nil {
			return nil, err
		}
		if !d.Delete(args[0]) {
			return nil, newErr(32811, "")
		}
		return nil, nil
	case "removeall":
		d.keys, d.vals, d.index = nil, nil, map[dictKey]int{}
		return nil, nil
	case "count":
		return int64(len(d.keys)), nil
	case "comparemode":
		if d.text {
			return int64(1), nil
		}
		return int64(0), nil
	}
	return nil, newErr(438, "")
}

// Set implements Object.
func (d *Dictionary) Set(name string, args []Value, v Value) error {
	switch name {
	case "", "item":
		if err := needArgs(args, 1, 1); err != nil {
			return err
		}
		k, err := d.key(args[0])
		if err != nil {
			return err
		}
		d.put(k, args[0], v)
		return nil
	case "key":
		if err := needArgs(args, 1, 1); err != nil {
			return err
		}
		oldK, err := d.key(args[0])
		if err != nil {
			return err
		}
		newK, err := d.key(v)
		if err != nil {
			return err
		}
		i, ok := d.index[oldK]
		if !ok {
			return newErr(32811, "")
		}
		if oldK == newK {
			d.keys[i] = v
			return nil
		}
		if _, dup := d.index[newK]; dup {
			return newErr(457, "")
		}
		delete(d.index, oldK)
		d.index[newK] = i
		d.keys[i] = v
		return nil
	case "comparemode":
		n, err := toInt(v)
		if err != nil {
			return err
		}
		if len(d.keys) > 0 || n < 0 || n > 2 {
			return newErr(5, "")
		}
		d.text = n == 1
		return nil
	}
	return newErr(438, "")
}
