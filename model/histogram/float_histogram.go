// Copyright 2021 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package histogram

import (
	"fmt"
	"math"
	"strings"
)

// FloatHistogram is similar to Histogram but uses float64 for all
// counts. Additionally, bucket counts are absolute and not deltas.
//
// A FloatHistogram is needed by PromQL to handle operations that might result
// in fractional counts. Since the counts in a histogram are unlikely to be too
// large to be represented precisely by a float64, a FloatHistogram can also be
// used to represent a histogram with integer counts and thus serves as a more
// generalized representation.
type FloatHistogram struct {
	// Currently valid schema numbers are -4 <= n <= 8.  They are all for
	// base-2 bucket schemas, where 1 is a bucket boundary in each case, and
	// then each power of two is divided into 2^n logarithmic buckets.  Or
	// in other words, each bucket boundary is the previous boundary times
	// 2^(2^-n).
	Schema int32
	// Width of the zero bucket.
	ZeroThreshold float64
	// Observations falling into the zero bucket. Must be zero or positive.
	ZeroCount float64
	// Total number of observations. Must be zero or positive.
	Count float64
	// Sum of observations. This is also used as the stale marker.
	Sum float64
	// Spans for positive and negative buckets (see Span below).
	PositiveSpans, NegativeSpans []Span
	// Observation counts in buckets. Each represents an absolute count and
	// must be zero or positive.
	PositiveBuckets, NegativeBuckets []float64
}

// Copy returns a deep copy of the Histogram.
func (h *FloatHistogram) Copy() *FloatHistogram {
	c := *h

	if h.PositiveSpans != nil {
		c.PositiveSpans = make([]Span, len(h.PositiveSpans))
		copy(c.PositiveSpans, h.PositiveSpans)
	}
	if h.NegativeSpans != nil {
		c.NegativeSpans = make([]Span, len(h.NegativeSpans))
		copy(c.NegativeSpans, h.NegativeSpans)
	}
	if h.PositiveBuckets != nil {
		c.PositiveBuckets = make([]float64, len(h.PositiveBuckets))
		copy(c.PositiveBuckets, h.PositiveBuckets)
	}
	if h.NegativeBuckets != nil {
		c.NegativeBuckets = make([]float64, len(h.NegativeBuckets))
		copy(c.NegativeBuckets, h.NegativeBuckets)
	}

	return &c
}

// String returns a string representation of the Histogram.
func (h *FloatHistogram) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "{count:%g, sum:%g", h.Count, h.Sum)

	var nBuckets []FloatBucket
	for it := h.NegativeBucketIterator(); it.Next(); {
		bucket := it.At()
		if bucket.Count != 0 {
			nBuckets = append(nBuckets, it.At())
		}
	}
	for i := len(nBuckets) - 1; i >= 0; i-- {
		fmt.Fprintf(&sb, ", %s", nBuckets[i].String())
	}

	if h.ZeroCount != 0 {
		fmt.Fprintf(&sb, ", %s", h.ZeroBucket().String())
	}

	for it := h.PositiveBucketIterator(); it.Next(); {
		bucket := it.At()
		if bucket.Count != 0 {
			fmt.Fprintf(&sb, ", %s", bucket.String())
		}
	}

	sb.WriteRune('}')
	return sb.String()
}

// ZeroBucket returns the zero bucket.
func (h *FloatHistogram) ZeroBucket() FloatBucket {
	return FloatBucket{
		Lower:          -h.ZeroThreshold,
		Upper:          h.ZeroThreshold,
		LowerInclusive: true,
		UpperInclusive: true,
		Count:          h.ZeroCount,
	}
}

// Scale scales the FloatHistogram by the provided factor, i.e. it scales all
// bucket counts including the zero bucket and the count and the sum of
// observations. The bucket layout stays the same. This method changes the
// receiving histogram directly (rather than acting on a copy). It returns a
// pointer to the receiving histogram for convenience.
func (h *FloatHistogram) Scale(factor float64) *FloatHistogram {
	h.ZeroCount *= factor
	h.Count *= factor
	h.Sum *= factor
	for i := range h.PositiveBuckets {
		h.PositiveBuckets[i] *= factor
	}
	for i := range h.NegativeBuckets {
		h.NegativeBuckets[i] *= factor
	}
	return h
}

// Add adds the provided other histogram to the receiving histogram. Count, Sum,
// and buckets from the other histogram are added to the corresponding
// components of the receiving histogram. Buckets in the other histogram that do
// not exist in the receiving histogram are inserted into the latter. The
// resulting histogram might have buckets with a population of zero or directly
// adjacent spans (offset=0). To normalize those, call the Compact method.
//
// This method returns a pointer to the receiving histogram for convenience.
//
// IMPORTANT: This method requires the Schema and the ZeroThreshold to be the
// same in both histograms. Otherwise, its behavior is undefined.
// TODO(beorn7): Change that!
func (h *FloatHistogram) Add(other *FloatHistogram) *FloatHistogram {
	h.ZeroCount += other.ZeroCount
	h.Count += other.Count
	h.Sum += other.Sum

	// TODO(beorn7): If needed, this can be optimized by inspecting the
	// spans in other and create missing buckets in h in batches.
	iSpan, iBucket := -1, -1
	var iInSpan, index int32
	for it := other.PositiveBucketIterator(); it.Next(); {
		b := it.At()
		h.PositiveSpans, h.PositiveBuckets, iSpan, iBucket, iInSpan = addBucket(
			b, h.PositiveSpans, h.PositiveBuckets, iSpan, iBucket, iInSpan, index,
		)
		index = b.Index
	}
	iSpan, iBucket = -1, -1
	for it := other.NegativeBucketIterator(); it.Next(); {
		b := it.At()
		h.NegativeSpans, h.NegativeBuckets, iSpan, iBucket, iInSpan = addBucket(
			b, h.NegativeSpans, h.NegativeBuckets, iSpan, iBucket, iInSpan, index,
		)
		index = b.Index
	}
	return h
}

// Sub works like Add but subtracts the other histogram.
//
// IMPORTANT: This method requires the Schema and the ZeroThreshold to be the
// same in both histograms. Otherwise, its behavior is undefined.
// TODO(beorn7): Change that!
func (h *FloatHistogram) Sub(other *FloatHistogram) *FloatHistogram {
	h.ZeroCount -= other.ZeroCount
	h.Count -= other.Count
	h.Sum -= other.Sum

	// TODO(beorn7): If needed, this can be optimized by inspecting the
	// spans in other and create missing buckets in h in batches.
	iSpan, iBucket := -1, -1
	var iInSpan, index int32
	for it := other.PositiveBucketIterator(); it.Next(); {
		b := it.At()
		b.Count *= -1
		h.PositiveSpans, h.PositiveBuckets, iSpan, iBucket, iInSpan = addBucket(
			b, h.PositiveSpans, h.PositiveBuckets, iSpan, iBucket, iInSpan, index,
		)
		index = b.Index
	}
	iSpan, iBucket = -1, -1
	for it := other.NegativeBucketIterator(); it.Next(); {
		b := it.At()
		b.Count *= -1
		h.NegativeSpans, h.NegativeBuckets, iSpan, iBucket, iInSpan = addBucket(
			b, h.NegativeSpans, h.NegativeBuckets, iSpan, iBucket, iInSpan, index,
		)
		index = b.Index
	}
	return h
}

// addBucket takes the "coordinates" of the last bucket that was handled and
// adds the provided bucket after it. If a corresponding bucket exists, the
// count is added. If not, the bucket is inserted. The updated slices and the
// coordinates of the inserted or added-to bucket are returned.
func addBucket(
	b FloatBucket,
	spans []Span, buckets []float64,
	iSpan, iBucket int,
	iInSpan, index int32,
) (
	newSpans []Span, newBuckets []float64,
	newISpan, newIBucket int, newIInSpan int32,
) {
	if iSpan == -1 {
		// First add, check if it is before all spans.
		if len(spans) == 0 || spans[0].Offset > b.Index {
			// Add bucket before all others.
			buckets = append(buckets, 0)
			copy(buckets[1:], buckets)
			buckets[0] = b.Count
			if spans[0].Offset == b.Index+1 {
				spans[0].Length++
				spans[0].Offset--
				return spans, buckets, 0, 0, 0
			}
			spans = append(spans, Span{})
			copy(spans[1:], spans)
			spans[0] = Span{Offset: b.Index, Length: 1}
			if len(spans) > 1 {
				// Convert the absolute offset in the formerly
				// first span to a relative offset.
				spans[1].Offset -= b.Index + 1
			}
			return spans, buckets, 0, 0, 0
		}
		if spans[0].Offset == b.Index {
			// Just add to first bucket.
			buckets[0] += b.Count
			return spans, buckets, 0, 0, 0
		}
		// We are behind the first bucket, so set everything to the
		// first bucket and continue normally.
		iSpan, iBucket, iInSpan = 0, 0, 0
		index = spans[0].Offset
	}
	deltaIndex := b.Index - index
	for {
		remainingInSpan := int32(spans[iSpan].Length) - iInSpan
		if deltaIndex < remainingInSpan {
			// Bucket is in current span.
			iBucket += int(deltaIndex)
			iInSpan += deltaIndex
			buckets[iBucket] += b.Count
			return spans, buckets, iSpan, iBucket, iInSpan
		}
		deltaIndex -= remainingInSpan
		iBucket += int(remainingInSpan)
		iSpan++
		if iSpan == len(spans) || deltaIndex < spans[iSpan].Offset {
			// Bucket is in gap behind previous span (or there are no further spans).
			buckets = append(buckets, 0)
			copy(buckets[iBucket+1:], buckets[iBucket:])
			buckets[iBucket] = b.Count
			if deltaIndex == 0 {
				// Directly after previous span, extend previous span.
				if iSpan < len(spans) {
					spans[iSpan].Offset--
				}
				iSpan--
				iInSpan = int32(spans[iSpan].Length)
				spans[iSpan].Length++
				return spans, buckets, iSpan, iBucket, iInSpan
			}
			if iSpan < len(spans) && deltaIndex == spans[iSpan].Offset-1 {
				// Directly before next span, extend next span.
				iInSpan = 0
				spans[iSpan].Offset--
				spans[iSpan].Length++
				return spans, buckets, iSpan, iBucket, iInSpan
			}
			// No next span, or next span is not directly adjacent to new bucket.
			// Add new span.
			iInSpan = 0
			if iSpan < len(spans) {
				spans[iSpan].Offset -= deltaIndex + 1
			}
			spans = append(spans, Span{})
			copy(spans[iSpan+1:], spans[iSpan:])
			spans[iSpan] = Span{Length: 1, Offset: deltaIndex}
			return spans, buckets, iSpan, iBucket, iInSpan
		}
		// Try start of next span.
		deltaIndex -= spans[iSpan].Offset
		iInSpan = 0
	}
}

// Compact eliminates empty buckets at the beginning and end of each span, then
// merges spans that are consecutive or at most maxEmptyBuckets apart, and
// finally splits spans that contain more consecutive empty buckets than
// maxEmptyBuckets. (The actual implementation might do something more efficient
// but with the same result.)  The compaction happens "in place" in the
// receiving histogram, but a pointer to it is returned for convenience.
func (h *FloatHistogram) Compact(maxEmptyBuckets int) *FloatHistogram {
	h.PositiveBuckets, h.PositiveSpans = compactBuckets(
		h.PositiveBuckets, h.PositiveSpans, maxEmptyBuckets,
	)
	h.NegativeBuckets, h.NegativeSpans = compactBuckets(
		h.NegativeBuckets, h.NegativeSpans, maxEmptyBuckets,
	)
	return h
}

func compactBuckets(buckets []float64, spans []Span, maxEmptyBuckets int) ([]float64, []Span) {
	if len(buckets) == 0 {
		return buckets, spans
	}

	var iBucket, iSpan int
	var posInSpan uint32

	// Helper function.
	emptyBucketsHere := func() int {
		i := 0
		for i+iBucket < len(buckets) &&
			uint32(i)+posInSpan < spans[iSpan].Length &&
			buckets[i+iBucket] == 0 {
			i++
		}
		return i
	}

	// Merge spans with zero-offset to avoid special cases later.
	if len(spans) > 1 {
		for i, span := range spans[1:] {
			if span.Offset == 0 {
				spans[iSpan].Length += span.Length
				continue
			}
			iSpan++
			if i+1 != iSpan {
				spans[iSpan] = span
			}
		}
		spans = spans[:iSpan+1]
		iSpan = 0
	}

	// Merge spans with zero-length to avoid special cases later.
	for i, span := range spans {
		if span.Length == 0 {
			if i+1 < len(spans) {
				spans[i+1].Offset += span.Offset
			}
			continue
		}
		if i != iSpan {
			spans[iSpan] = span
		}
		iSpan++
	}
	spans = spans[:iSpan]
	iSpan = 0

	// Cut out empty buckets from start and end of spans, no matter
	// what. Also cut out empty buckets from the middle of a span but only
	// if there are more than maxEmptyBuckets consecutive empty buckets.
	for iBucket < len(buckets) {
		if nEmpty := emptyBucketsHere(); nEmpty > 0 {
			if posInSpan > 0 &&
				nEmpty < int(spans[iSpan].Length-posInSpan) &&
				nEmpty <= maxEmptyBuckets {
				// The empty buckets are in the middle of a
				// span, and there are few enough to not bother.
				// Just fast-forward.
				iBucket += nEmpty
				posInSpan += uint32(nEmpty)
				continue
			}
			// In all other cases, we cut out the empty buckets.
			buckets = append(buckets[:iBucket], buckets[iBucket+nEmpty:]...)
			if posInSpan == 0 {
				// Start of span.
				if nEmpty == int(spans[iSpan].Length) {
					// The whole span is empty.
					spans = append(spans[:iSpan], spans[iSpan+1:]...)
					continue
				}
				spans[iSpan].Length -= uint32(nEmpty)
				spans[iSpan].Offset += int32(nEmpty)
				continue
			}
			// It's in the middle or in the end of the span.
			// Split the current span.
			newSpan := Span{
				Offset: int32(nEmpty),
				Length: spans[iSpan].Length - posInSpan - uint32(nEmpty),
			}
			spans[iSpan].Length = posInSpan
			// In any case, we have to split to the next span.
			iSpan++
			posInSpan = 0
			if newSpan.Length == 0 {
				// The span is empty, so we were already at the end of a span.
				// We don't have to insert the new span, just adjust the next
				// span's offset, if there is one.
				if iSpan < len(spans) {
					spans[iSpan].Offset += int32(nEmpty)
				}
				continue
			}
			// Insert the new span.
			spans = append(spans, Span{})
			if iSpan+1 < len(spans) {
				copy(spans[iSpan+1:], spans[iSpan:])
			}
			spans[iSpan] = newSpan
			continue
		}
		iBucket++
		posInSpan++
		if posInSpan >= spans[iSpan].Length {
			posInSpan = 0
			iSpan++
		}
	}
	if maxEmptyBuckets == 0 {
		return buckets, spans
	}

	// Finally, check if any offsets between spans are small enough to merge
	// the spans.
	iBucket = int(spans[0].Length)
	iSpan = 1
	for iSpan < len(spans) {
		if int(spans[iSpan].Offset) > maxEmptyBuckets {
			iBucket += int(spans[iSpan].Length)
			iSpan++
			continue
		}
		// Merge span with previous one and insert empty buckets.
		offset := int(spans[iSpan].Offset)
		spans[iSpan-1].Length += uint32(offset) + spans[iSpan].Length
		spans = append(spans[:iSpan], spans[iSpan+1:]...)
		newBuckets := make([]float64, len(buckets)+offset)
		copy(newBuckets, buckets[:iBucket])
		copy(newBuckets[iBucket+offset:], buckets[iBucket:])
		iBucket += offset
		buckets = newBuckets
		// Note that with many merges, it would be more efficient to
		// first record all the chunks of empty buckets to insert and
		// then do it in one go through all the buckets.
	}

	return buckets, spans
}

// DetectReset returns true if the receiving histogram is missing any buckets
// that have a non-zero population in the provided previous histogram. It also
// returns true if any count (in any bucket, in the zero count, or in the count
// of observations, but NOT the sum of observations) is smaller in the receiving
// histogram compared to the previous histogram. Otherwise, it returns false.
//
// Special behavior in case the Schema or the ZeroThreshold are not the same in
// both histograms:
//
// * A decrease of the ZeroThreshold or an increase of the Schema (i.e. an
//   increase of resolution) can only happen together with a reset. Thus, the
//   method returns true in either case.
//
// * Upon an increase of the ZeroThreshold, the buckets in the previous
//   histogram that fall within the new ZeroThreshold are added to the ZeroCount
//   of the previous histogram (without mutating the provided previous
//   histogram). The scenario that a populated bucket of the previous histogram
//   is partially within, partially outside of the new ZeroThreshold, can only
//   happen together with a counter reset and therefore shortcuts to returning
//   true.
//
// * Upon a decrease of the Schema, the buckets of the previous histogram are
//   merged so that they match the new, lower-resolution schema (again without
//   mutating the provided previous histogram).
//
// Note that this kind of reset detection is quite expensive. Ideally, resets
// are detected at ingest time and stored in the TSDB, so that the reset
// information can be read directly from there rather than be detected each time
// again.
func (h *FloatHistogram) DetectReset(previous *FloatHistogram) bool {
	if h.Count < previous.Count {
		return true
	}
	if h.Schema > previous.Schema {
		return true
	}
	previousZeroCount, ok := previous.zeroCountForLargerThreshold(h.ZeroThreshold)
	if !ok {
		// ZeroThreshold decreased or is within a populated bucket in
		// previous histogram.
		return true
	}
	if h.ZeroCount < previousZeroCount {
		return true
	}
	currIt := h.floatBucketIterator(true, h.ZeroThreshold, h.Schema)
	prevIt := previous.floatBucketIterator(true, h.ZeroThreshold, h.Schema)
	if detectReset(currIt, prevIt) {
		return true
	}
	currIt = h.floatBucketIterator(false, h.ZeroThreshold, h.Schema)
	prevIt = previous.floatBucketIterator(false, h.ZeroThreshold, h.Schema)
	return detectReset(currIt, prevIt)
}

func detectReset(currIt, prevIt FloatBucketIterator) bool {
	if !prevIt.Next() {
		return false // If no buckets in previous histogram, nothing can be reset.
	}
	prevBucket := prevIt.At()
	if !currIt.Next() {
		// No bucket in current, but at least one in previous
		// histogram. Check if any of those are non-zero, in which case
		// this is a reset.
		for {
			if prevBucket.Count != 0 {
				return true
			}
			if !prevIt.Next() {
				return false
			}
		}
	}
	currBucket := currIt.At()
	for {
		// Forward currIt until we find the bucket corresponding to prevBucket.
		for currBucket.Index < prevBucket.Index {
			if !currIt.Next() {
				// Reached end of currIt early, therefore
				// previous histogram has a bucket that the
				// current one does not have. Unlass all
				// remaining buckets in the previous histogram
				// are unpopulated, this is a reset.
				for {
					if prevBucket.Count != 0 {
						return true
					}
					if !prevIt.Next() {
						return false
					}
				}
			}
			currBucket = currIt.At()
		}
		if currBucket.Index > prevBucket.Index {
			// Previous histogram has a bucket the current one does
			// not have. If it's populated, it's a reset.
			if prevBucket.Count != 0 {
				return true
			}
		} else {
			// We have reached corresponding buckets in both iterators.
			// We can finally compare the counts.
			if currBucket.Count < prevBucket.Count {
				return true
			}
		}
		if !prevIt.Next() {
			// Reached end of prevIt without finding offending buckets.
			return false
		}
		prevBucket = prevIt.At()
	}
}

// PositiveBucketIterator returns a FloatBucketIterator to iterate over all
// positive buckets in ascending order (starting next to the zero bucket and
// going up).
func (h *FloatHistogram) PositiveBucketIterator() FloatBucketIterator {
	return h.floatBucketIterator(true, 0, h.Schema)
}

// NegativeBucketIterator returns a FloatBucketIterator to iterate over all
// negative buckets in descending order (starting next to the zero bucket and
// going down).
func (h *FloatHistogram) NegativeBucketIterator() FloatBucketIterator {
	return h.floatBucketIterator(false, 0, h.Schema)
}

// PositiveReverseBucketIterator returns a FloatBucketIterator to iterate over all
// positive buckets in descending order (starting at the highest bucket and going
// down towards the zero bucket).
func (h *FloatHistogram) PositiveReverseBucketIterator() FloatBucketIterator {
	return h.reverseFloatBucketIterator(true)
}

// NegativeReverseBucketIterator returns a FloatBucketIterator to iterate over all
// negative buckets in ascending order (starting at the lowest bucket and going up
// towards the zero bucket).
func (h *FloatHistogram) NegativeReverseBucketIterator() FloatBucketIterator {
	return h.reverseFloatBucketIterator(false)
}

// AllBucketIterator returns a FloatBucketIterator to iterate over all negative,
// zero, and positive buckets in ascending order (starting at the lowest bucket
// and going up).
func (h *FloatHistogram) AllBucketIterator() FloatBucketIterator {
	return &allFloatBucketIterator{
		h:       h,
		negIter: h.NegativeReverseBucketIterator(),
		posIter: h.PositiveBucketIterator(),
		state:   -1,
	}
}

// CumulativeBucketIterator returns a FloatBucketIterator to iterate over a
// cumulative view of the buckets. This method currently only supports
// FloatHistograms without negative buckets and panics if the FloatHistogram has
// negative buckets. It is currently only used for testing.
func (h *FloatHistogram) CumulativeBucketIterator() FloatBucketIterator {
	if len(h.NegativeBuckets) > 0 {
		panic("CumulativeBucketIterator called on FloatHistogram with negative buckets")
	}
	return &cumulativeFloatBucketIterator{h: h, posSpansIdx: -1}
}

// floatBucketIterator is a low-level constructor for bucket iterators.
//
// If positive is true, the returned iterator iterates through the positive
// buckets, otherwise through the negative buckets.
//
// If absoluteStartValue is ≤ the lowest absolute value of any lower bucket
// boundary, the iterator starts with the first bucket. Otherwise, it will skip
// all buckets with an absolute value of their upper boundary ≤
// absoluteStartValue.
//
// targetSchema must be ≤ the schema of FloatHistogram (and of course within the
// legal values for schemas in general). The buckets are merged to match the
// targetSchema prior to iterating (without mutating FloatHistogram).
func (h *FloatHistogram) floatBucketIterator(
	positive bool, absoluteStartValue float64, targetSchema int32,
) *floatBucketIterator {
	if targetSchema > h.Schema {
		panic(fmt.Errorf("cannot merge from schema %d to %d", h.Schema, targetSchema))
	}
	i := &floatBucketIterator{schema: h.Schema, targetSchema: targetSchema, positive: positive}
	if positive {
		i.spans = h.PositiveSpans
		i.buckets = h.PositiveBuckets
	} else {
		i.spans = h.NegativeSpans
		i.buckets = h.NegativeBuckets
	}
	if len(i.buckets) == 0 {
		// No buckets to skip.
		return i
	}
	if getBound(i.spans[0].Offset-1, h.Schema) >= absoluteStartValue {
		// The first bucket's lower boundary is at least
		// absoluteStartValue. Nothing to skip.
		return i
	}
	// If we are here, there is at least one bucket to skip.
	for i.Next() {
		if i.bucketsIdx+1 >= len(i.buckets) {
			// No more buckets to skip.
			return i
		}
		nextIdx := i.currIdx + 1
		if i.idxInSpan+1 >= i.spans[i.spansIdx].Length {
			nextIdx += i.spans[i.spansIdx+1].Offset
		}
		if getBound(nextIdx, h.Schema) > absoluteStartValue {
			// Next bucket hav an upper bound greater than
			// absoluteStartValue. Thus, we are done.
			return i
		}
	}
	return i
}

// reverseFloatbucketiterator is a low-level constructor for reverse bucket iterators.
func (h *FloatHistogram) reverseFloatBucketIterator(positive bool) *reverseFloatBucketIterator {
	r := &reverseFloatBucketIterator{schema: h.Schema, positive: positive}
	if positive {
		r.spans = h.PositiveSpans
		r.buckets = h.PositiveBuckets
	} else {
		r.spans = h.NegativeSpans
		r.buckets = h.NegativeBuckets
	}

	r.spansIdx = len(r.spans) - 1
	r.bucketsIdx = len(r.buckets) - 1
	if r.spansIdx >= 0 {
		r.idxInSpan = int32(r.spans[r.spansIdx].Length) - 1
	}
	r.currIdx = 0
	for _, s := range r.spans {
		r.currIdx += s.Offset + int32(s.Length)
	}

	return r
}

// zeroCountForLargerThreshold returns what the histogram's zero count would be
// if the ZeroThreshold had the provided larger (or equal) value. If the
// provided value is less than the histogram's ZeroThreshold, or if the provided
// threshold ends up within a populated bucket of the histogram, the methods
// returns (0, false).
func (h *FloatHistogram) zeroCountForLargerThreshold(threshold float64) (count float64, ok bool) {
	if threshold < h.ZeroThreshold {
		return 0, false
	}
	// TODO: Implement.
	return h.ZeroCount, true
}

// FloatBucketIterator iterates over the buckets of a FloatHistogram, returning
// decoded buckets.
type FloatBucketIterator interface {
	// Next advances the iterator by one.
	Next() bool
	// At returns the current bucket.
	At() FloatBucket
}

// FloatBucket represents a bucket with lower and upper limit and the count of
// samples in the bucket as a float64. It also specifies if each limit is
// inclusive or not. (Mathematically, inclusive limits create a closed interval,
// and non-inclusive limits an open interval.)
//
// To represent cumulative buckets, Lower is set to -Inf, and the Count is then
// cumulative (including the counts of all buckets for smaller values).
type FloatBucket struct {
	Lower, Upper                   float64
	LowerInclusive, UpperInclusive bool
	Count                          float64

	// Index within schema. To easily compare buckets that share the same
	// schema and sign (positive or negative). Irrelevant for the zero bucket.
	Index int32
}

// String returns a string representation of a FloatBucket, using the usual
// mathematical notation of '['/']' for inclusive bounds and '('/')' for
// non-inclusive bounds.
func (b FloatBucket) String() string {
	var sb strings.Builder
	if b.LowerInclusive {
		sb.WriteRune('[')
	} else {
		sb.WriteRune('(')
	}
	fmt.Fprintf(&sb, "%g,%g", b.Lower, b.Upper)
	if b.UpperInclusive {
		sb.WriteRune(']')
	} else {
		sb.WriteRune(')')
	}
	fmt.Fprintf(&sb, ":%g", b.Count)
	return sb.String()
}

type floatBucketIterator struct {
	// targetSchema is the schema to merge to and must be ≤ schema.
	schema, targetSchema int32
	spans                []Span
	buckets              []float64

	positive bool // Whether this is for positive buckets.

	spansIdx   int    // Current span within spans slice.
	idxInSpan  uint32 // Index in the current span. 0 <= idxInSpan < span.Length.
	bucketsIdx int    // Current bucket within buckets slice.

	currCount            float64 // Count in the current bucket.
	currIdx              int32   // The actual bucket index.
	currLower, currUpper float64 // Limits of the current bucket.
}

func (i *floatBucketIterator) Next() bool {
	// TODO: Use targetSchema.
	if i.spansIdx >= len(i.spans) {
		return false
	}
	span := i.spans[i.spansIdx]
	// Seed currIdx for the first bucket.
	if i.bucketsIdx == 0 {
		i.currIdx = span.Offset
	} else {
		i.currIdx++
	}
	for i.idxInSpan >= span.Length {
		// We have exhausted the current span and have to find a new
		// one. We'll even handle pathologic spans of length 0.
		i.idxInSpan = 0
		i.spansIdx++
		if i.spansIdx >= len(i.spans) {
			return false
		}
		span = i.spans[i.spansIdx]
		i.currIdx += span.Offset
	}

	i.currCount = i.buckets[i.bucketsIdx]
	if i.positive {
		i.currUpper = getBound(i.currIdx, i.schema)
		i.currLower = getBound(i.currIdx-1, i.schema)
	} else {
		i.currLower = -getBound(i.currIdx, i.schema)
		i.currUpper = -getBound(i.currIdx-1, i.schema)
	}

	i.idxInSpan++
	i.bucketsIdx++
	return true
}

func (i *floatBucketIterator) At() FloatBucket {
	return FloatBucket{
		Count:          i.currCount,
		Lower:          i.currLower,
		Upper:          i.currUpper,
		LowerInclusive: i.currLower < 0,
		UpperInclusive: i.currUpper > 0,
		Index:          i.currIdx,
	}
}

type reverseFloatBucketIterator struct {
	schema  int32
	spans   []Span
	buckets []float64

	positive bool // Whether this is for positive buckets.

	spansIdx   int   // Current span within spans slice.
	idxInSpan  int32 // Index in the current span. 0 <= idxInSpan < span.Length.
	bucketsIdx int   // Current bucket within buckets slice.

	currCount            float64 // Count in the current bucket.
	currIdx              int32   // The actual bucket index.
	currLower, currUpper float64 // Limits of the current bucket.
}

func (r *reverseFloatBucketIterator) Next() bool {
	r.currIdx--
	if r.bucketsIdx < 0 {
		return false
	}

	for r.idxInSpan < 0 {
		// We have exhausted the current span and have to find a new
		// one. We'll even handle pathologic spans of length 0.
		r.spansIdx--
		r.idxInSpan = int32(r.spans[r.spansIdx].Length) - 1
		r.currIdx -= r.spans[r.spansIdx+1].Offset
	}

	r.currCount = r.buckets[r.bucketsIdx]
	if r.positive {
		r.currUpper = getBound(r.currIdx, r.schema)
		r.currLower = getBound(r.currIdx-1, r.schema)
	} else {
		r.currLower = -getBound(r.currIdx, r.schema)
		r.currUpper = -getBound(r.currIdx-1, r.schema)
	}

	r.bucketsIdx--
	r.idxInSpan--
	return true
}

func (r *reverseFloatBucketIterator) At() FloatBucket {
	return FloatBucket{
		Count:          r.currCount,
		Lower:          r.currLower,
		Upper:          r.currUpper,
		LowerInclusive: r.currLower < 0,
		UpperInclusive: r.currUpper > 0,
		Index:          r.currIdx,
	}
}

type allFloatBucketIterator struct {
	h                *FloatHistogram
	negIter, posIter FloatBucketIterator
	// -1 means we are iterating negative buckets.
	// 0 means it is time for the zero bucket.
	// 1 means we are iterating positive buckets.
	// Anything else means iteration is over.
	state      int8
	currBucket FloatBucket
}

func (r *allFloatBucketIterator) Next() bool {
	switch r.state {
	case -1:
		if r.negIter.Next() {
			r.currBucket = r.negIter.At()
			return true
		}
		r.state = 0
		return r.Next()
	case 0:
		r.state = 1
		if r.h.ZeroCount > 0 {
			r.currBucket = FloatBucket{
				Lower:          -r.h.ZeroThreshold,
				Upper:          r.h.ZeroThreshold,
				LowerInclusive: true,
				UpperInclusive: true,
				Count:          r.h.ZeroCount,
				// Index is irrelevant for the zero bucket.
			}
			return true
		}
		return r.Next()
	case 1:
		if r.posIter.Next() {
			r.currBucket = r.posIter.At()
			return true
		}
		r.state = 42
		return false
	}

	return false
}

func (r *allFloatBucketIterator) At() FloatBucket {
	return r.currBucket
}

type cumulativeFloatBucketIterator struct {
	h *FloatHistogram

	posSpansIdx   int    // Index in h.PositiveSpans we are in. -1 means 0 bucket.
	posBucketsIdx int    // Index in h.PositiveBuckets.
	idxInSpan     uint32 // Index in the current span. 0 <= idxInSpan < span.Length.

	initialized         bool
	currIdx             int32   // The actual bucket index after decoding from spans.
	currUpper           float64 // The upper boundary of the current bucket.
	currCumulativeCount float64 // Current "cumulative" count for the current bucket.

	// Between 2 spans there could be some empty buckets which
	// still needs to be counted for cumulative buckets.
	// When we hit the end of a span, we use this to iterate
	// through the empty buckets.
	emptyBucketCount int32
}

func (c *cumulativeFloatBucketIterator) Next() bool {
	if c.posSpansIdx == -1 {
		// Zero bucket.
		c.posSpansIdx++
		if c.h.ZeroCount == 0 {
			return c.Next()
		}

		c.currUpper = c.h.ZeroThreshold
		c.currCumulativeCount = c.h.ZeroCount
		return true
	}

	if c.posSpansIdx >= len(c.h.PositiveSpans) {
		return false
	}

	if c.emptyBucketCount > 0 {
		// We are traversing through empty buckets at the moment.
		c.currUpper = getBound(c.currIdx, c.h.Schema)
		c.currIdx++
		c.emptyBucketCount--
		return true
	}

	span := c.h.PositiveSpans[c.posSpansIdx]
	if c.posSpansIdx == 0 && !c.initialized {
		// Initializing.
		c.currIdx = span.Offset
		c.initialized = true
	}

	c.currCumulativeCount += c.h.PositiveBuckets[c.posBucketsIdx]
	c.currUpper = getBound(c.currIdx, c.h.Schema)

	c.posBucketsIdx++
	c.idxInSpan++
	c.currIdx++
	if c.idxInSpan >= span.Length {
		// Move to the next span. This one is done.
		c.posSpansIdx++
		c.idxInSpan = 0
		if c.posSpansIdx < len(c.h.PositiveSpans) {
			c.emptyBucketCount = c.h.PositiveSpans[c.posSpansIdx].Offset
		}
	}

	return true
}

func (c *cumulativeFloatBucketIterator) At() FloatBucket {
	return FloatBucket{
		Upper:          c.currUpper,
		Lower:          math.Inf(-1),
		UpperInclusive: true,
		LowerInclusive: true,
		Count:          c.currCumulativeCount,
		Index:          c.currIdx - 1,
	}
}
