package gatewaylite

// Failed/cancelled batches do not fill settled_at. The release dedup fact and
// balance refund are committed in one transaction; both live and archive dedup
// tables are authoritative. Output deletion preserves successful settlement.
const bindingBatchImageOutstandingSQL = `SELECT count(*) FROM batch_image_jobs j
WHERE NOT (
 (j.status IN ('completed','output_deleted') AND j.settled_at IS NOT NULL)
 OR (j.status IN ('failed','cancelled') AND (
  COALESCE(j.hold_amount,j.estimated_cost,0)<=0
  OR EXISTS(SELECT 1 FROM usage_billing_dedup d WHERE d.request_id='batch_image_release:' || j.batch_id AND d.api_key_id=j.api_key_id)
  OR EXISTS(SELECT 1 FROM usage_billing_dedup_archive d WHERE d.request_id='batch_image_release:' || j.batch_id AND d.api_key_id=j.api_key_id)
 ))
)`
