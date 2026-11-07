-- +goose Up
-- One word per thing: the daily test that going back works is the check,
-- in the schema, the activity and the alert events too.
ALTER TABLE backup_status RENAME COLUMN verified_at TO checked_at;
ALTER TABLE backup_status RENAME COLUMN drill_error TO check_error;

UPDATE activity_events SET action = 'check.failed' WHERE action = 'drill.failed';
UPDATE activity_events SET action = 'check.recovered' WHERE action = 'drill.recovered';
UPDATE activity_events
SET action = 'backup.checked', metadata = REPLACE(metadata, '"verified_at":', '"checked_at":')
WHERE action = 'backup.drill_passed';

UPDATE alert_destinations
SET events = REPLACE(REPLACE(events, 'drill.failed', 'check.failed'), 'drill.recovered', 'check.recovered');

-- +goose Down
UPDATE alert_destinations
SET events = REPLACE(REPLACE(events, 'check.failed', 'drill.failed'), 'check.recovered', 'drill.recovered');

UPDATE activity_events
SET action = 'backup.drill_passed', metadata = REPLACE(metadata, '"checked_at":', '"verified_at":')
WHERE action = 'backup.checked';
UPDATE activity_events SET action = 'drill.recovered' WHERE action = 'check.recovered';
UPDATE activity_events SET action = 'drill.failed' WHERE action = 'check.failed';

ALTER TABLE backup_status RENAME COLUMN check_error TO drill_error;
ALTER TABLE backup_status RENAME COLUMN checked_at TO verified_at;
