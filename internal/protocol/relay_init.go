package protocol

import "strconv"

// initReply keeps the response representation tied to the original client's request.
func (r *Relay) initReply(u UsageContext, o RelayOptions) {
	if o.legacyFunctions && !o.Aggregate {
		r.legacy = &rewriter{legacyDelta: &legacyReplyDelta{}}
		r.legacy.charge = r.charge
	}
	r.up = r.format.newUpstream(u, o, r.charge)
	if o.Aggregate {
		r.agg = newAggregator(r.client, r.charge)
		if a, ok := r.agg.(*chatAggregate); ok {
			a.legacy = o.legacyFunctions
			a.healing = u.ResponseHealing
			a.cited = len(o.Citations) > 0
			if o.Cached {
				a.created = []byte(strconv.FormatInt(o.Created, 10))
			}
		}
		if a, ok := r.agg.(*responsesAggregate); ok && o.Cached {
			a.createdAt = []byte(strconv.FormatInt(o.Created, 10))
		}
	}
}
