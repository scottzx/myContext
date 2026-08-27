-- ===========================================================================
-- 010_opportunity_case.sql
--
-- The V1a business root: an Opportunity you can actually work from.
--
-- Two link tables close the gaps 005 left, and then a family of views projects
-- one continuous story out of tables that stay normalised. The views are the
-- deliverable: "case" is a PROJECTION, not a table (design premise 6). There is
-- no `cases` row anywhere, and React never re-implements membership or ordering.
-- ===========================================================================

-- ---------------------------------------------------------------------------
-- opportunity_projects: the pre-sales chain 005 was missing.
--
-- A project advances a deal; the deal records what is true, the project records
-- what is being done about it. This is the ONLY authority for that link -
-- nothing writes it into a subject column or a soft edge as well.
--
-- Primary is one-to-one in both directions. One project quietly advancing three
-- deals is how a pipeline number becomes a lie; when execution genuinely is
-- shared, the honest answer is an Initiative above both, not a fan-out here.
-- 'support' exists only so migration has somewhere to put historic rows; the
-- V1a confirm UI never offers it.
-- ---------------------------------------------------------------------------
CREATE TABLE opportunity_projects (
    opportunity_id TEXT NOT NULL REFERENCES opportunities(id),
    project_id     TEXT NOT NULL REFERENCES projects(id),
    role           TEXT NOT NULL DEFAULT 'primary' CHECK (role IN ('primary','support')),
    created_at     TEXT NOT NULL,
    PRIMARY KEY (opportunity_id, project_id)
);

CREATE UNIQUE INDEX ux_opportunity_projects_primary_opp
    ON opportunity_projects(opportunity_id) WHERE role = 'primary';
CREATE UNIQUE INDEX ux_opportunity_projects_primary_proj
    ON opportunity_projects(project_id) WHERE role = 'primary';
CREATE INDEX ix_opportunity_projects_project ON opportunity_projects(project_id);

-- ---------------------------------------------------------------------------
-- interaction_documents: which document IS the transcript of which meeting.
--
-- A typed link rather than another entry in the global polymorphic entity
-- vocabulary: interactions appear in exactly one place, so widening
-- doc_links.entity_type (and with it every CHECK that mirrors it) buys nothing.
-- role is what makes the timeline able to say "逐字稿" instead of "attachment".
-- ---------------------------------------------------------------------------
CREATE TABLE interaction_documents (
    interaction_id TEXT NOT NULL REFERENCES interactions(id),
    document_id    TEXT NOT NULL REFERENCES documents(id),
    role           TEXT NOT NULL
                   CHECK (role IN ('transcript','minutes','attachment','evidence')),
    created_at     TEXT NOT NULL,
    PRIMARY KEY (interaction_id, document_id, role)
);

CREATE INDEX ix_interaction_documents_document ON interaction_documents(document_id);

-- ---------------------------------------------------------------------------
-- v_case_root_membership: which rows belong to which case, resolved once.
--
-- Every other case view reads this. path_rank encodes "direct beats indirect":
-- a document attached to the opportunity itself outranks the same document
-- reached through its project, so the de-duplication below keeps the more
-- meaningful path rather than an arbitrary one.
--
-- Note what is NOT here: the account's other opportunities. An account-level
-- event belongs to the account, and pulling its whole history into one deal
-- would make two unrelated deals with the same customer look identical.
-- ---------------------------------------------------------------------------
CREATE VIEW v_case_root_membership AS
SELECT root_type, root_id, member_type, member_id, MIN(path_rank) AS path_rank
  FROM (
-- The root itself.
SELECT 'opportunity' AS root_type, o.id AS root_id,
       'opportunity'  AS member_type, o.id AS member_id, 0 AS path_rank
  FROM opportunities o
UNION ALL
-- Projects that advance it.
SELECT 'opportunity', op.opportunity_id, 'project', op.project_id, 1
  FROM opportunity_projects op
UNION ALL
-- Contracts that came out of it.
SELECT 'opportunity', k.opportunity_id, 'contract', k.id, 1
  FROM contracts k
 WHERE k.opportunity_id IS NOT NULL
UNION ALL
-- Milestones and tasks of those projects.
SELECT 'opportunity', op.opportunity_id, 'milestone', m.id, 2
  FROM opportunity_projects op
  JOIN milestones m ON m.project_id = op.project_id
UNION ALL
SELECT 'opportunity', op.opportunity_id, 'task', t.id, 2
  FROM opportunity_projects op
  JOIN tasks t ON t.project_id = op.project_id
UNION ALL
-- Tasks pointed straight at the deal, whatever project schedules them.
SELECT 'opportunity', t.subject_id, 'task', t.id, 1
  FROM tasks t
 WHERE t.subject_type = 'opportunity' AND t.subject_id IS NOT NULL
UNION ALL
-- Money against those contracts.
SELECT 'opportunity', k.opportunity_id, 'receipt', r.id, 2
  FROM contracts k
  JOIN receipts r ON r.contract_id = k.id
 WHERE k.opportunity_id IS NOT NULL
UNION ALL
-- Interactions about the deal.
SELECT 'opportunity', i.subject_id, 'interaction', i.id, 1
  FROM interactions i
 WHERE i.subject_type = 'opportunity' AND i.subject_id IS NOT NULL
UNION ALL
-- Documents attached to the deal directly.
SELECT 'opportunity', dl.entity_id, 'document', dl.doc_id, 1
  FROM doc_links dl
 WHERE dl.entity_type = 'opportunity'
UNION ALL
-- Documents that are the transcript/minutes of one of its interactions.
SELECT 'opportunity', i.subject_id, 'document', idoc.document_id, 2
  FROM interactions i
  JOIN interaction_documents idoc ON idoc.interaction_id = i.id
 WHERE i.subject_type = 'opportunity' AND i.subject_id IS NOT NULL
UNION ALL
-- Documents reached through a project that advances it.
SELECT 'opportunity', op.opportunity_id, 'document', dl.doc_id, 3
  FROM opportunity_projects op
  JOIN doc_links dl ON dl.entity_type = 'project' AND dl.entity_id = op.project_id
)
 GROUP BY root_type, root_id, member_type, member_id;

-- ---------------------------------------------------------------------------
-- v_case_timeline: what happened, in order, in one context.
--
-- occurred_at follows the design's time priority per item type: an event or
-- interaction happened when it happened; a document falls back captured_at then
-- created_at because "when it is about" is the better answer when present; a
-- milestone enters history only once it was actually reached, because a future
-- date is a plan and belongs in Next Node instead.
--
-- sort_priority breaks ties within the same timestamp so a confirm that writes
-- an object, its interaction and its documents in one transaction still reads
-- top-to-bottom in a sensible order rather than by rowid.
--
-- No de-duplication happens here: v_case_root_membership already resolved each
-- row to one path, which is the only place that judgement should live.
-- ---------------------------------------------------------------------------
CREATE VIEW v_case_timeline AS
SELECT * FROM (
    -- Audit events on anything in the case.
    SELECT mb.root_type, mb.root_id, 'event' AS item_type, e.id AS item_id,
           e.occurred_at                                  AS occurred_at,
           e.entity_type || ' ' || e.event_type           AS title,
           e.reason                                       AS summary,
           e.actor_type                                   AS actor,
           NULL                                           AS document_id,
           0                                              AS source_count,
           e.correlation_id                               AS correlation_id,
           3                                              AS sort_priority
      FROM v_case_root_membership mb
      JOIN events e ON e.entity_type = mb.member_type AND e.entity_id = mb.member_id
    UNION ALL
    -- Meetings, calls, messages.
    SELECT mb.root_type, mb.root_id, 'interaction', i.id,
           i.occurred_at, i.channel, i.summary, i.owner,
           NULL,
           (SELECT COUNT(*) FROM interaction_documents d WHERE d.interaction_id = i.id),
           NULL, 1
      FROM v_case_root_membership mb
      JOIN interactions i ON i.id = mb.member_id
     WHERE mb.member_type = 'interaction'
    UNION ALL
    -- Artefact versions: the proposal v1 -> v2 chain reads here.
    SELECT mb.root_type, mb.root_id, 'document', d.id,
           COALESCE(d.occurred_at, d.captured_at, d.created_at),
           d.title, d.change_note, d.author_name,
           d.id,
           (SELECT COUNT(*) FROM source_attributions sa WHERE sa.document_id = d.id),
           NULL, 2
      FROM v_case_root_membership mb
      JOIN documents d ON d.id = mb.member_id
     WHERE mb.member_type = 'document'
    UNION ALL
    -- Only milestones already reached: a future one is a plan, not history, and
    -- belongs in v_case_next_action instead.
    SELECT mb.root_type, mb.root_id, 'milestone', m.id,
           m.reached_at, m.name, m.note, NULL,
           NULL, 0, NULL, 0
      FROM v_case_root_membership mb
      JOIN milestones m ON m.id = mb.member_id
     WHERE mb.member_type = 'milestone' AND m.reached_at IS NOT NULL
    UNION ALL
    -- Money actually arriving.
    SELECT mb.root_type, mb.root_id, 'receipt', r.id,
           r.received_at, 'receipt ' || CAST(r.amount AS TEXT), r.note, r.method,
           NULL, 0, NULL, 0
      FROM v_case_root_membership mb
      JOIN receipts r ON r.id = mb.member_id
     WHERE mb.member_type = 'receipt'
    UNION ALL
    -- Contract signature: the state change a deal is judged by.
    SELECT mb.root_type, mb.root_id, 'contract', k.id,
           COALESCE(k.sign_date, k.created_at), k.name, k.status, NULL,
           NULL, 0, NULL, 0
      FROM v_case_root_membership mb
      JOIN contracts k ON k.id = mb.member_id
     WHERE mb.member_type = 'contract'
)
 WHERE occurred_at IS NOT NULL;

-- ---------------------------------------------------------------------------
-- v_case_next_action: the top of the workspace, and on mobile the first thing
-- on screen. Everything here is in the FUTURE or open right now - it is the
-- complement of the timeline, not a slice of it.
-- ---------------------------------------------------------------------------
CREATE VIEW v_case_next_action AS
SELECT r.root_type,
       r.root_id,
       (SELECT MIN(m.target_date)
          FROM v_case_root_membership mb
          JOIN milestones m ON m.id = mb.member_id
         WHERE mb.root_type = r.root_type AND mb.root_id = r.root_id
           AND mb.member_type = 'milestone'
           AND m.status IN ('pending','at_risk'))                       AS next_milestone_at,
       (SELECT m.name
          FROM v_case_root_membership mb
          JOIN milestones m ON m.id = mb.member_id
         WHERE mb.root_type = r.root_type AND mb.root_id = r.root_id
           AND mb.member_type = 'milestone'
           AND m.status IN ('pending','at_risk')
         ORDER BY m.target_date LIMIT 1)                                AS next_milestone_name,
       (SELECT MIN(COALESCE(s.planned_date, date(t.hard_due_at), t.next_review_at))
          FROM v_case_root_membership mb
          JOIN tasks t ON t.id = mb.member_id
          LEFT JOIN task_schedules s ON s.task_id = t.id AND s.status = 'active'
         WHERE mb.root_type = r.root_type AND mb.root_id = r.root_id
           AND mb.member_type = 'task'
           AND t.status NOT IN ('done','cancelled','archived'))         AS next_action_at,
       (SELECT COUNT(*)
          FROM v_case_root_membership mb
          JOIN tasks t ON t.id = mb.member_id
         WHERE mb.root_type = r.root_type AND mb.root_id = r.root_id
           AND mb.member_type = 'task'
           AND t.status NOT IN ('done','cancelled','archived'))         AS open_task_count,
       (SELECT COUNT(*)
          FROM v_case_root_membership mb
          JOIN tasks t ON t.id = mb.member_id
         WHERE mb.root_type = r.root_type AND mb.root_id = r.root_id
           AND mb.member_type = 'task'
           AND t.status NOT IN ('done','cancelled','archived')
           AND t.hard_due_at IS NOT NULL
           AND date(t.hard_due_at) < (SELECT today FROM v_clock))        AS overdue_count
  FROM (SELECT DISTINCT root_type, root_id FROM v_case_root_membership) r;

-- ---------------------------------------------------------------------------
-- v_case_index: the list every case is reached from. One shape, several kinds
-- of root - V1b unions external_program and standalone application in here
-- without any consumer having to learn a second schema.
-- ---------------------------------------------------------------------------
CREATE VIEW v_case_index AS
SELECT 'opportunity'                          AS root_type,
       o.id                                   AS root_id,
       o.name                                 AS title,
       'tob'                                  AS kind,
       o.stage                                AS stage,
       o.owner                                AS owner,
       CASE WHEN o.stage IN ('negotiation','proposal') THEN 'P1' ELSE 'P2' END AS importance,
       (SELECT op.project_id FROM opportunity_projects op
         WHERE op.opportunity_id = o.id AND op.role = 'primary')        AS primary_project_id,
       a.name                                 AS counterparty_name,
       o.expected_sign_date                   AS next_review_at,
       na.next_milestone_at,
       na.next_milestone_name,
       na.next_action_at,
       (SELECT MAX(i.occurred_at) FROM interactions i
         WHERE i.subject_type = 'opportunity' AND i.subject_id = o.id)   AS last_interaction_at,
       (SELECT MAX(tl.occurred_at) FROM v_case_timeline tl
         WHERE tl.root_type = 'opportunity' AND tl.root_id = o.id
           AND tl.item_type = 'document')                               AS last_evidence_at,
       COALESCE(na.open_task_count, 0)        AS open_task_count,
       COALESCE(na.overdue_count, 0)          AS overdue_count,
       (SELECT COUNT(*) FROM v_case_quality q
         WHERE q.root_type = 'opportunity' AND q.root_id = o.id)         AS warning_count
  FROM opportunities o
  JOIN accounts a ON a.id = o.account_id
  LEFT JOIN v_case_next_action na
         ON na.root_type = 'opportunity' AND na.root_id = o.id;

-- ---------------------------------------------------------------------------
-- v_case_evidence: every confirmed field of the case, with the byte range it
-- came from. Whether an attribution is still CURRENT is decided by comparing
-- normalized_value_hash against the field's normalised value - and normalising
-- is the Field Registry's job in Go, not something to re-implement in SQL where
-- it would inevitably drift from the registry it is supposed to mirror.
-- ---------------------------------------------------------------------------
CREATE VIEW v_case_evidence AS
SELECT mb.root_type,
       mb.root_id,
       sa.entity_type,
       sa.entity_id,
       sa.field_name,
       sa.normalized_value_hash,
       sa.document_id,
       sa.source_locator_json,
       sa.origin_type,
       sa.created_at,
       d.title AS document_title
  FROM source_attributions sa
  JOIN v_case_root_membership mb
    ON mb.member_type = sa.entity_type AND mb.member_id = sa.entity_id
  LEFT JOIN documents d ON d.id = sa.document_id;

-- ---------------------------------------------------------------------------
-- v_inbox_pending: what still needs a human. Split by why, because "extract it"
-- and "review it" and "it broke" are three different actions.
-- ---------------------------------------------------------------------------
CREATE VIEW v_inbox_pending AS
SELECT i.id            AS inbox_id,
       i.title,
       i.source_ref,
       i.status,
       i.capture_kind,
       i.document_id,
       i.package_id,
       i.assigned_root_type,
       i.assigned_root_id,
       i.error_code,
       i.error_message,
       i.version,
       i.created_at,
       i.updated_at,
       (SELECT r.id FROM extraction_runs r
         WHERE r.inbox_id = i.id AND r.status = 'completed'
         ORDER BY r.completed_at DESC LIMIT 1)                          AS active_run_id,
       (SELECT COUNT(*) FROM extraction_runs r
         WHERE r.inbox_id = i.id AND r.status = 'running')              AS running_count,
       (SELECT COUNT(*) FROM entity_candidates c
          JOIN extraction_runs r ON r.id = c.run_id
         WHERE r.inbox_id = i.id AND c.status = 'proposed')             AS undecided_entities,
       (SELECT COUNT(*) FROM fact_candidates c
          JOIN extraction_runs r ON r.id = c.run_id
         WHERE r.inbox_id = i.id AND c.status = 'proposed')             AS undecided_facts,
       (SELECT COUNT(*) FROM relation_candidates c
          JOIN extraction_runs r ON r.id = c.run_id
         WHERE r.inbox_id = i.id AND c.status = 'proposed')             AS undecided_relations,
       (SELECT COUNT(*) FROM action_candidates c
          JOIN extraction_runs r ON r.id = c.run_id
         WHERE r.inbox_id = i.id AND c.status = 'proposed')             AS undecided_actions
  FROM inbox_items i
 WHERE i.status IN ('captured','extracting','reviewing','error');

-- ---------------------------------------------------------------------------
-- v_opportunity_health: the four ways a deal quietly dies. Stated, not fixed.
-- ---------------------------------------------------------------------------
CREATE VIEW v_opportunity_health AS
SELECT o.id                                    AS opportunity_id,
       o.name,
       o.stage,
       o.account_id,
       ci.last_interaction_at,
       ci.next_action_at,
       CAST(julianday((SELECT today FROM v_clock)) -
            julianday(COALESCE(date(ci.last_interaction_at), date(o.created_at)))
            AS INTEGER)                        AS days_since_interaction,
       (o.next_step IS NULL OR o.next_step = '') AS missing_next_step,
       (o.expected_sign_date IS NULL)          AS missing_expected_sign_date,
       (ci.primary_project_id IS NULL)         AS missing_primary_project,
       (ci.next_action_at IS NULL)             AS missing_next_action
  FROM opportunities o
  JOIN v_case_index ci ON ci.root_type = 'opportunity' AND ci.root_id = o.id
 WHERE o.stage NOT IN ('won','lost');

-- ---------------------------------------------------------------------------
-- v_candidate_decision_drift: the candidate's cached status disagrees with the
-- latest decision appended for it.
--
-- Decisions are the authority and `status` is a read cache maintained in the
-- same transaction. If these ever diverge, something wrote a status outside the
-- confirm path - which is exactly the failure the whole layer is built to make
-- impossible, so it is an integrity error, not a warning.
-- ---------------------------------------------------------------------------
CREATE VIEW v_candidate_decision_drift AS
WITH latest AS (
    SELECT d.candidate_type, d.candidate_id, d.decision, d.decided_at,
           ROW_NUMBER() OVER (PARTITION BY d.candidate_type, d.candidate_id
                              ORDER BY d.decided_at DESC, d.id DESC) AS rn
      FROM candidate_decisions d
), all_candidates AS (
    SELECT 'entity'   AS candidate_type, id, status FROM entity_candidates
    UNION ALL SELECT 'fact',     id, status FROM fact_candidates
    UNION ALL SELECT 'relation', id, status FROM relation_candidates
    UNION ALL SELECT 'action',   id, status FROM action_candidates
)
SELECT c.candidate_type, c.id AS candidate_id, c.status, l.decision, l.decided_at
  FROM all_candidates c
  LEFT JOIN latest l ON l.candidate_type = c.candidate_type
                    AND l.candidate_id = c.id AND l.rn = 1
 WHERE (l.decision = 'accept' AND c.status <> 'accepted')
    OR (l.decision = 'reject' AND c.status <> 'rejected')
    OR (l.decision IS NULL AND c.status IN ('accepted','rejected'));

-- ---------------------------------------------------------------------------
-- v_intake_quality_issues: the intake layer's own checks.
--
-- Kept beside v_data_quality_issues and v_biz_quality_issues rather than folded
-- into either: those two answer "is the work hygienic" and "do the numbers add
-- up". These answer a harder question - "did a rule that was supposed to hold
-- inside a transaction actually hold". A row here is closer to an integrity
-- failure than to a nudge.
--
-- Grandfathering is deliberate. Rows that predate this migration never went
-- through confirm and have no attributions; holding them to the new rule would
-- produce a wall of alarms about data that was correct when it was written. The
-- last check below therefore only looks at objects confirm itself created.
-- ---------------------------------------------------------------------------
CREATE VIEW v_intake_quality_issues AS
SELECT 'candidate' AS entity_type, candidate_id AS entity_id, candidate_type AS title,
       'candidate_decision_drift' AS issue,
       'candidate status disagrees with its latest recorded decision' AS detail
  FROM v_candidate_decision_drift
UNION ALL
-- Bytes are safe in the Library but nothing points at them yet.
SELECT 'inbox', id, COALESCE(title, id), 'sealed_without_registration',
       'library package was sealed but its document/inbox registration never completed'
  FROM capture_ingestions
 WHERE state = 'sealed'
UNION ALL
SELECT 'inbox', id, COALESCE(title, id), 'capture_failed',
       'a capture ended in failure and left no usable inbox item'
  FROM capture_ingestions
 WHERE state = 'failed'
UNION ALL
SELECT 'inbox', i.id, COALESCE(i.title, i.id), 'confirmed_without_root',
       'inbox item is confirmed but was never routed to a business root'
  FROM inbox_items i
 WHERE i.status = 'confirmed' AND i.assigned_root_id IS NULL
UNION ALL
SELECT 'inbox', i.id, COALESCE(i.title, i.id), 'inbox_error',
       'capture or extraction failed for this item and it is waiting on a human'
  FROM inbox_items i
 WHERE i.status = 'error'
UNION ALL
-- An attribution still claiming to be current for a relation that is gone.
SELECT 'relation', ra.id, ra.relation_type, 'dangling_relation_attribution',
       'attribution is still current but the relation it documents no longer exists'
  FROM relation_attributions ra
 WHERE ra.valid_to_correlation_id IS NULL
   AND ra.storage_type = 'opportunity_projects'
   -- Compared through storage_key, not through from_id/to_id: the endpoints are
   -- in relation direction (a project advances an opportunity) while the row is
   -- keyed the way the table stores it, and matching them positionally is how
   -- this check would silently report every healthy link as dangling.
   AND NOT EXISTS (SELECT 1 FROM opportunity_projects op
                    WHERE op.opportunity_id || ':' || op.project_id = ra.storage_key)
UNION ALL
SELECT 'opportunity', o.id, o.name, 'confirmed_field_without_source',
       'opportunity was created through inbox confirm but carries no source attribution'
  FROM opportunities o
 WHERE EXISTS (SELECT 1 FROM entity_candidates c
                WHERE c.materialized_type = 'opportunity' AND c.materialized_id = o.id)
   AND NOT EXISTS (SELECT 1 FROM source_attributions sa
                    WHERE sa.entity_type = 'opportunity' AND sa.entity_id = o.id)
UNION ALL
-- Confirm writes the object change and its event under one correlation id. A
-- materialised object with no such event means something wrote it another way.
SELECT 'opportunity', o.id, o.name, 'materialized_without_event',
       'confirm materialised this object but no event records the change'
  FROM opportunities o
  JOIN entity_candidates c ON c.materialized_type = 'opportunity' AND c.materialized_id = o.id
 WHERE NOT EXISTS (SELECT 1 FROM events e
                    WHERE e.entity_type = 'opportunity' AND e.entity_id = o.id);

-- ---------------------------------------------------------------------------
-- v_case_quality: the warnings a workspace header shows, per root. It reads
-- both existing quality views plus the intake one, so a case page never has to
-- know which view a given warning came from.
-- ---------------------------------------------------------------------------
CREATE VIEW v_case_quality AS
SELECT mb.root_type, mb.root_id, q.entity_type, q.entity_id, q.title, q.issue, q.detail
  FROM (SELECT entity_type, entity_id, title, issue, detail FROM v_biz_quality_issues
        UNION ALL
        SELECT entity_type, entity_id, title, issue, detail FROM v_intake_quality_issues) q
  JOIN v_case_root_membership mb
    ON mb.member_type = q.entity_type AND mb.member_id = q.entity_id;
