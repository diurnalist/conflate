package conflate

import (
	"fmt"
	"reflect"

	"github.com/mitchellh/hashstructure/v2"
)

type mergeContext struct {
	sliceMergeBehavior SliceMergeBehavior
	ctx                context
}

func mergeTo(mc mergeContext, toData interface{}, fromData ...interface{}) error {
	for _, fromDatum := range fromData {
		err := merge(mc, toData, fromDatum)
		if err != nil {
			return err
		}
	}

	return nil
}

func merge(mc mergeContext, pToData, fromData interface{}) error {
	mc.ctx = rootContext()
	return mergeRecursive(mc, pToData, fromData)
}

func mergeRecursive(mc mergeContext, pToData, fromData interface{}) error {
	if pToData == nil {
		return &errWithContext{
			context: mc.ctx,
			msg:     "the destination variable must not be nil",
		}
	}

	pToVal := reflect.ValueOf(pToData)
	if pToVal.Kind() != reflect.Ptr {
		return &errWithContext{
			context: mc.ctx,
			msg:     "the destination variable must be a pointer",
		}
	}

	if fromData == nil {
		return nil
	}

	toVal := pToVal.Elem()
	fromVal := reflect.ValueOf(fromData)

	toData := toVal.Interface()

	if toVal.Interface() == nil {
		toVal.Set(fromVal)

		return nil
	}

	var err error

	//nolint:exhaustive // to be refactored
	switch fromVal.Kind() {
	case reflect.Map:
		err = mergeMapRecursive(mc, toData, fromData)
	case reflect.Slice:
		err = mergeSliceRecursive(mc, toVal, toData, fromData)
	default:
		err = mergeDefaultRecursive(mc, toVal, fromVal, toData, fromData)
	}

	return err
}

func mergeMapRecursive(mc mergeContext, toData, fromData interface{}) error {
	fromProps, ok := fromData.(map[string]interface{})
	if !ok {
		return &errWithContext{
			context: mc.ctx,
			msg:     "the source value must be a map[string]interface{}",
		}
	}

	toProps, _ := toData.(map[string]interface{})
	if toProps == nil {
		return &errWithContext{
			context: mc.ctx,
			msg:     "the destination value must be a map[string]interface{}",
		}
	}

	for name, fromProp := range fromProps {
		if val := toProps[name]; val == nil {
			toProps[name] = fromProp
		} else {
			err := merge(mc, &val, fromProp)
			if err != nil {
				return &errWithContext{
					context: mc.ctx.add(name),
					msg:     fmt.Sprintf("failed to merge object property : %v : %v", name, err.Error()),
				}
			}

			toProps[name] = val
		}
	}

	return nil
}

func mergeSliceRecursive(mc mergeContext, toVal reflect.Value, toData, fromData interface{}) error {
	fromItems, ok := fromData.([]interface{})
	if !ok {
		return &errWithContext{
			context: mc.ctx,
			msg:     "the source value must be a []interface{}",
		}
	}

	toItems, _ := toData.([]interface{})
	if toItems == nil {
		return &errWithContext{
			context: mc.ctx,
			msg:     "the destination value must be a []interface{}",
		}
	}

	var fromById = map[interface{}]interface{}{}
	var toById = map[interface{}]interface{}{}
	var seen = map[uint64][]int{}
	addById(fromItems, fromById)
	addById(toItems, toById)

	var newItems []interface{}
	for idx, item := range toItems {
		id := getId(item)
		merged := false
		if id != nil {
			from := fromById[id]
			to := toById[id]
			if from != nil && to != nil {
				err := merge(mc, &to, from)
				if err != nil {
					return err
				}
				newItems = append(newItems, to)
				merged = true
			}
		}
		if !merged {
			hash, err := hashstructure.Hash(item, hashstructure.FormatV2, nil)
			if err != nil {
				return err
			}
			seen[hash] = append(seen[hash], idx)
			newItems = append(newItems, item)
		}
	}
	for _, item := range fromItems {
		id := getId(item)
		skipped := false
		if id != nil {
			from := fromById[id]
			to := toById[id]
			if from != nil && to != nil {
				// merged in last loop
				skipped = true
			}
		}
		hash, err := hashstructure.Hash(item, hashstructure.FormatV2, nil)
		if err != nil {
			return err
		}
		if len(seen[hash]) > 0 {
			seen[hash] = seen[hash][1:] // shift the first seen index off
			skipped = true
		}

		if !skipped {
			newItems = append(newItems, item)
		}
	}

	var mergedItems []interface{}
	if mc.sliceMergeBehavior == SliceMergeBehaviorOverride {
		// clean up any orphan values in destination
		sentinel := struct{}{}
		for _, idxs := range seen {
			for _, idx := range idxs {
				newItems[idx] = sentinel
			}
		}
		for _, item := range newItems {
			if item != sentinel {
				mergedItems = append(mergedItems, item)
			}
		}
	} else {
		mergedItems = newItems
	}

	toVal.Set(reflect.ValueOf(mergedItems))

	return nil
}

func addById(items []interface{}, target map[interface{}]interface{}) {
	for _, item := range items {
		id := getId(item)
		if id != nil {
			target[id] = item
		}
	}
}

func getId(item interface{}) interface{} {
	props, ok := item.(map[string]interface{})
	if ok {
		ids := []string{"id", "refId", "name"}
		for _, key := range ids {
			v := props[key]
			if v != nil {
				return v
			}
		}
	}
	return nil
}

func mergeDefaultRecursive(mc mergeContext, toVal, fromVal reflect.Value, toData, fromData interface{}) error {
	if reflect.DeepEqual(toData, fromData) {
		return nil
	}

	fromType := fromVal.Type()
	toType := toVal.Type()

	if toType.Kind() == reflect.Interface {
		toType = toVal.Elem().Type()
	}

	if !fromType.AssignableTo(toType) {
		return &errWithContext{
			context: mc.ctx,
			msg:     fmt.Sprintf("the destination type (%v) must be the same as the source type (%v)", toType, fromType),
		}
	}

	toVal.Set(fromVal)

	return nil
}
