package conflate

import (
	"fmt"
	"reflect"

	"github.com/mitchellh/hashstructure/v2"
)

func mergeTo(toData interface{}, fromData ...interface{}) error {
	for _, fromDatum := range fromData {
		err := merge(toData, fromDatum)
		if err != nil {
			return err
		}
	}

	return nil
}

func merge(pToData, fromData interface{}) error {
	return mergeRecursive(rootContext(), pToData, fromData)
}

func mergeRecursive(ctx context, pToData, fromData interface{}) error {
	if pToData == nil {
		return &errWithContext{
			context: ctx,
			msg:     "the destination variable must not be nil",
		}
	}

	pToVal := reflect.ValueOf(pToData)
	if pToVal.Kind() != reflect.Ptr {
		return &errWithContext{
			context: ctx,
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
		err = mergeMapRecursive(ctx, toData, fromData)
	case reflect.Slice:
		err = mergeSliceRecursive(ctx, toVal, toData, fromData)
	default:
		err = mergeDefaultRecursive(ctx, toVal, fromVal, toData, fromData)
	}

	return err
}

func mergeMapRecursive(ctx context, toData, fromData interface{}) error {
	fromProps, ok := fromData.(map[string]interface{})
	if !ok {
		return &errWithContext{
			context: ctx,
			msg:     "the source value must be a map[string]interface{}",
		}
	}

	toProps, _ := toData.(map[string]interface{})
	if toProps == nil {
		return &errWithContext{
			context: ctx,
			msg:     "the destination value must be a map[string]interface{}",
		}
	}

	for name, fromProp := range fromProps {
		if val := toProps[name]; val == nil {
			toProps[name] = fromProp
		} else {
			err := merge(&val, fromProp)
			if err != nil {
				return &errWithContext{
					context: ctx.add(name),
					msg:     fmt.Sprintf("failed to merge object property : %v : %v", name, err.Error()),
				}
			}

			toProps[name] = val
		}
	}

	return nil
}

func mergeSliceRecursive(ctx context, toVal reflect.Value, toData, fromData interface{}) error {
	fromItems, ok := fromData.([]interface{})
	if !ok {
		return &errWithContext{
			context: ctx,
			msg:     "the source value must be a []interface{}",
		}
	}

	toItems, _ := toData.([]interface{})
	if toItems == nil {
		return &errWithContext{
			context: ctx,
			msg:     "the destination value must be a []interface{}",
		}
	}

	var fromById = map[interface{}]interface{}{}
	var toById = map[interface{}]interface{}{}
	var seen = map[uint64]int{}
	addById(fromItems, fromById)
	addById(toItems, toById)

	var newItems []interface{}
	for _, item := range toItems {
		id := getId(item)
		merged := false
		if id != nil {
			from := fromById[id]
			to := toById[id]
			if from != nil && to != nil {
				err := merge(&to, from)
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
			seen[hash] += 1
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
		if seen[hash] > 0 {
			seen[hash] -= 1
			skipped = true
		}

		if !skipped {
			newItems = append(newItems, item)
		}
	}

	toVal.Set(reflect.ValueOf(newItems))

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

func mergeDefaultRecursive(ctx context, toVal, fromVal reflect.Value, toData, fromData interface{}) error {
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
			context: ctx,
			msg:     fmt.Sprintf("the destination type (%v) must be the same as the source type (%v)", toType, fromType),
		}
	}

	toVal.Set(fromVal)

	return nil
}
