package cron

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/fwtpe/owl-backend/common/model"
	"github.com/fwtpe/owl-backend/modules/alarm/g"
	eventmodel "github.com/fwtpe/owl-backend/modules/alarm/model/event"
	"github.com/garyburd/redigo/redis"
)

func ReadHighEvent() {
	queues := g.Config().Redis.HighQueues
	if len(queues) == 0 {
		return
	}

	for {
		event, err := popEvent(queues)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		consume(event, true)
	}
}

func ReadLowEvent() {
	queues := g.Config().Redis.LowQueues
	if len(queues) == 0 {
		return
	}

	for {
		event, err := popEvent(queues)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		consume(event, false)
	}
}

func popEvent(queues []string) (*model.Event, error) {
	count := len(queues)

	params := make([]interface{}, count+1)
	for i := 0; i < count; i++ {
		params[i] = queues[i]
	}
	// set timeout 0
	params[count] = 0

	rc := g.RedisConnPool.Get()
	defer rc.Close()

	reply, err := redis.Strings(rc.Do("BRPOP", params...))
	if err != nil {
		log.Errorf("[REDIS BRPOP] has error. [%v] Redis Param: [%v]. ", err, params)
		return nil, err
	}

	var event model.Event
	err = json.Unmarshal([]byte(reply[1]), &event)
	if err != nil {
		log.Errorf("Unmarshal JSON of event has error: %v", err)
		return nil, err
	}

	go logTooLateMetric(&event)

	log.Debug(event.String())
	//insert event into database
	err = eventmodel.InsertEvent(&event, "owl")
	if err != nil {
		log.Error(err.Error())
	}
	// save in memory. display in dashboard
	g.Events.Put(&event)

	return &event, nil
}

func ReadExternalEvent() {
	queues := g.Config().Redis.ExternalQueues.Queues
	if len(queues) == 0 {
		return
	}

	for {
		err := popExternalEvent(queues)
		if err != nil {
			log.Errorf("[popExternalEvent] %v", err)
			time.Sleep(time.Second)
			continue
		}
	}
}

func popExternalEvent(queues []string) error {
	count := len(queues)

	params := make([]interface{}, count+1)
	for i := 0; i < count; i++ {
		params[i] = queues[i]
	}
	// set timeout 0
	params[count] = 0

	rc := g.RedisConnPool.Get()
	defer rc.Close()

	reply, err := redis.Strings(rc.Do("BRPOP", params...))
	if err != nil {
		log.Errorf("get alarm event from redis fail: %v", err)
		return err
	}

	event := eventmodel.ExternalEvent{}

	err = json.Unmarshal([]byte(reply[1]), &event)
	if err != nil {
		log.Errorf("parse alarm event fail: %v", err)
		return err
	}
	if err := event.CheckFormating(); err != nil {
		errMsg := fmt.Sprintf("check alarm formating got error: %v, event: %v", err, event)
		if g.Config().Redis.ErrorQueue.Enable {
			params := []interface{}{g.Config().Redis.ErrorQueue.Queue, errMsg}
			_, err := rc.Do("LPUSH", params...)
			if err != nil {
				log.Errorf("[Radis LPUSH<Error Queue>] %v", err)
			}
		}
		return err
	}

	event = event.ForceFixStepWhenStatusOk()
	//insert event into database
	err = eventmodel.InsertExternalEvent(event)
	if err != nil {
		log.Errorf("InsertExternalEvent() has error %v", err)
		errMsg := fmt.Sprintf("insert event got error: %v, event: %v", err.Error(), event)
		if g.Config().Redis.ErrorQueue.Enable {
			params := []interface{}{g.Config().Redis.ErrorQueue.Queue, errMsg}
			_, err := rc.Do("LPUSH", params...)
			if err != nil {
				log.Errorf("[Radis LPUSH<Error Queue>][ForceFixStepWhenStatusOk] %v", err)
			}
		}
	}

	return nil
}

const (
	tooLateSecond = 180
	tooLateTime   = tooLateSecond * time.Second
)

func logTooLateMetric(eventData *model.Event) {
	now := time.Now()

	reachTransferTime := time.Unix(eventData.ReachTransferTime, 0)
	diffTime := now.Sub(reachTransferTime)
	if diffTime > tooLateTime {
		log.Warnf(
			"[Too Long(%d{%d} seconds)] Reach/Now[%s - %s] Metric[%s]",
			diffTime/time.Second, tooLateSecond,
			reachTransferTime.Format(time.RFC3339), now.Format(time.RFC3339),
			eventData,
		)
	}
}
