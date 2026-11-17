CREATE TABLE job_receipts (
			job_id text primary key,
			kind text not null,
			state text not null
				check (state in ('processing', 'succeeded', 'failed', 'dead')),
			attempts integer not null default 0 check (attempts >= 0),
			last_error text,
			first_seen_at datetime not null,
			updated_at datetime not null,
			completed_at datetime
		);
CREATE INDEX idx_job_receipts_state_updated_at
			on job_receipts(state, updated_at);
CREATE TABLE job_attempts (
			id integer primary key autoincrement,
			job_id text not null,
			attempt integer not null,
			started_at datetime not null,
			finished_at datetime,
			result text not null check (result in ('running', 'succeeded', 'failed')),
			error text,
			foreign key (job_id) references job_receipts(job_id)
		);
CREATE INDEX idx_job_attempts_job_id on job_attempts(job_id);
CREATE TABLE dead_letters (
			job_id text primary key,
			kind text not null,
			payload_json text not null,
			error text not null,
			created_at datetime not null
		);
