-- ops.db v5: modes B and C. 005 gave the database a business core built around
-- a counterparty and a signed amount. That shape answers "who is this for, did
-- the money arrive" and cannot answer the other two business lines:
--
--   B content       we publish on our own accounts, and the return is an
--                   audience number, not an invoice
--   C product       we build 听记, ship versions of it, and push it online and
--                   offline; the return is downloads and DAU
--
-- Four objects carry both:
--
--   channels        our own publishing accounts. Followers are a series in
--                   metric_samples(subject_type='channel'), never a column.
--   content_pieces  one post, from 选题 to published. "选题" is simply
--                   status='idea' - there is no topics table, because a topic
--                   that gets written IS the piece, and copying it into a
--                   second row would create two ids for one thing.
--   releases        one shipped version of a product. The engineering that
--                   produced it is a project; this row is the result.
--   campaigns       one promotion push, online or offline. It serves a product
--                   AND content: the 杭州 developer meetup and a 小红书 push
--                   are the same object with a different channel_type.
--
-- What this migration deliberately does NOT do:
--
--   * It does not rebuild a single existing table. 005 already wrote
--     'channel', 'content_piece', 'release' and 'campaign' into every CHECK
--     vocabulary that needed them - tags, events, dependencies, doc_links,
--     metric_samples.subject_type, interactions.subject_type, context_edges -
--     precisely so that this file would not have to. `products` is extended
--     with ALTER TABLE ADD COLUMN and keeps its rowids, its indexes and its
--     data.
--
--   * It does not add a trigger, a derived column or a rollup table. Every
--     number below is computed in a VIEW at read time, and where a declared
--     value exists beside a computed one - products.current_release_id versus
--     the newest actually-released release - BOTH are shown and neither is
--     overwritten. That rule comes from v_metric_rollup (004:531) and
--     v_contract_receivable (005), and it does not get an exception here.
--
--   * It does not total anything across business lines. A contract amount, a
--     prize and an impression count are not the same kind of quantity.
--     v_content_pipeline groups by channel, v_campaign_effect keeps money
--     (budget) in a column beside the metric and never in the same sum, and
--     v_campaign_effect states a before and an after WITHOUT claiming the
--     campaign caused the difference.
--
-- The one existing view this file replaces is v_entity_index, and replacing it
-- is the point: it is the SQL mirror of the Go `entityTables` map, and the
-- three dangling checks in v_biz_quality_issues (dangling_context_edge,
-- dangling_doc_link, dangling_task_subject) are written against it. Adding the
-- four new types there is what makes those checks cover content and product;
-- no new dangling logic is written below. v_biz_quality_issues is dropped and
-- recreated only because it reads v_entity_index and because five new rules
-- join it.

-- Windows the new quality rules need, kept in the database like the ones 005
-- added, so the number is visible rather than buried in a view.
INSERT INTO ops_settings (key, value, updated_at) VALUES
    ('biz_content_metric_days', '7', '1970-01-01T00:00:00Z'),
    ('biz_campaign_metric_days', '7', '1970-01-01T00:00:00Z');

-- ===========================================================================
-- Mode B: content.
-- ===========================================================================

-- ---------------------------------------------------------------------------
-- channels: OUR accounts, not other people's. A 小红书 account we post to is a
-- channel; a media outlet we want coverage from is an `accounts` row with
-- account_type='media'. The distinction matters because a channel has a
-- follower series we own and can measure, and an outlet does not.
--
-- There is no follower_count column. Follower counts change weekly and a
-- single column would answer "how many" while destroying "is it growing",
-- which is the only question worth asking; the series lives in
-- metric_samples(subject_type='channel') and v_channel_growth reads it.
-- ---------------------------------------------------------------------------
CREATE TABLE channels (
    id         TEXT PRIMARY KEY,
    platform   TEXT NOT NULL
               CHECK (platform IN ('xiaohongshu','wechat','douyin','bilibili','x','offline')),
    name       TEXT NOT NULL CHECK (name <> ''),
    -- The account id on that platform. Unique per platform when present: two
    -- rows for one handle are a duplicate, not two channels.
    handle     TEXT,
    status     TEXT NOT NULL DEFAULT 'active'
               CHECK (status IN ('active','paused','archived')),
    owner      TEXT,
    note       TEXT,
    legacy_ref TEXT,
    version    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX ix_channels_status ON channels(status, platform);
CREATE UNIQUE INDEX ux_channels_handle ON channels(platform, handle) WHERE handle IS NOT NULL;

-- ===========================================================================
-- Mode C: product. releases and campaigns come before content_pieces because
-- a piece references both.
-- ===========================================================================

-- ---------------------------------------------------------------------------
-- releases: one shipped version. The development work is a project with tasks
-- and milestones; this row is what came out of it, which is why it has a
-- status and a date and no effort fields.
--
-- The version string is `version_label`, NOT `version`: every mutable table in
-- this schema uses `version INTEGER` for optimistic concurrency (patch.go:86
-- writes `version = version + 1 WHERE id = ? AND version = ?`), and a release
-- is mutable - planned -> developing -> released. Two different meanings of
-- one column name is how a write path silently corrupts a row.
--
-- UNIQUE(product_id, version_label): v0.3.0 of 听记 exists once. The same
-- string under a different product is a different release.
-- ---------------------------------------------------------------------------
CREATE TABLE releases (
    id            TEXT PRIMARY KEY,
    product_id    TEXT NOT NULL REFERENCES products(id),
    version_label TEXT NOT NULL
                  CHECK (version_label <> '' AND version_label = trim(version_label)),
    status        TEXT NOT NULL DEFAULT 'planned'
                  CHECK (status IN ('planned','developing','released','rolled_back')),
    -- Shipping is a fact with a date; so is pulling it back. Neither state is
    -- allowed to exist without one.
    released_at   TEXT,
    notes_doc_id  TEXT REFERENCES documents(id),
    note          TEXT,
    legacy_ref    TEXT,
    version       INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    CHECK (status NOT IN ('released','rolled_back') OR released_at IS NOT NULL)
);
CREATE UNIQUE INDEX ux_releases_version ON releases(product_id, version_label);
CREATE INDEX ix_releases_product ON releases(product_id, status, released_at);

-- ---------------------------------------------------------------------------
-- campaigns: one promotion push. Serves mode C (a product launch) and mode B
-- (a content push) with the same object, because a 杭州 offline developer
-- meetup and a paid 小红书 slot differ by channel_type and by nothing else
-- structurally.
--
-- product_id is nullable: brand-level content promotion pushes no single
-- product. budget is what we planned to spend and stays a plain number beside
-- the metrics; nothing in this file adds it to an impression count.
-- ---------------------------------------------------------------------------
CREATE TABLE campaigns (
    id           TEXT PRIMARY KEY,
    product_id   TEXT REFERENCES products(id),
    name         TEXT NOT NULL CHECK (name <> ''),
    channel_type TEXT NOT NULL DEFAULT 'online'
                 CHECK (channel_type IN ('online','offline')),
    start_date   TEXT CHECK (start_date IS NULL OR
                             start_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    end_date     TEXT CHECK (end_date IS NULL OR
                             end_date   GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    budget       REAL CHECK (budget IS NULL OR budget >= 0),
    owner        TEXT,
    status       TEXT NOT NULL DEFAULT 'planned'
                 CHECK (status IN ('planned','running','ended','cancelled')),
    note         TEXT,
    legacy_ref   TEXT,
    version      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    -- A campaign that is running or over has a window; a planned or cancelled
    -- one may not have decided on dates yet. v_campaign_effect needs the
    -- window to exist before it can state a before and an after.
    CHECK (status IN ('planned','cancelled') OR start_date IS NOT NULL),
    CHECK (status <> 'ended' OR end_date IS NOT NULL),
    CHECK (start_date IS NULL OR end_date IS NULL OR start_date <= end_date)
);
CREATE INDEX ix_campaigns_status ON campaigns(status, start_date);
CREATE INDEX ix_campaigns_product ON campaigns(product_id) WHERE product_id IS NOT NULL;
CREATE INDEX ix_campaigns_window ON campaigns(start_date, end_date)
    WHERE status NOT IN ('planned','cancelled');

-- ---------------------------------------------------------------------------
-- content_pieces: one post, from the moment it is an idea to the moment it is
-- published, in ONE row.
--
-- "选题" is status='idea'. A separate topics table would mean promoting a
-- topic into a piece - a second id for the same thing, and every metric,
-- document link and task pointing at whichever of the two the writer happened
-- to have. The status column is the whole difference.
--
-- The body is not here either: it is documents(kind='content_draft'), reached
-- through draft_doc_id, so that a piece gets versions, renditions and a change
-- note for free from the document machinery in 005.
--
-- channel_id is NULLABLE on purpose - an idea often precedes the decision of
-- where it goes - and v_biz_quality_issues states the gap instead of a CHECK
-- refusing the write.
-- ---------------------------------------------------------------------------
CREATE TABLE content_pieces (
    id            TEXT PRIMARY KEY,
    channel_id    TEXT REFERENCES channels(id),
    title         TEXT NOT NULL CHECK (title <> ''),
    -- The angle, in the writer's words. Distinct from title: several pieces
    -- can work the same topic from different angles.
    topic         TEXT,
    product_id    TEXT REFERENCES products(id),
    campaign_id   TEXT REFERENCES campaigns(id),
    status        TEXT NOT NULL DEFAULT 'idea'
                  CHECK (status IN ('idea','drafting','review','scheduled','published','archived')),
    -- RFC3339 UTC, like tasks.hard_due_at: "past its scheduled time" is a
    -- question about an instant, not a day.
    scheduled_for TEXT,
    published_at  TEXT,
    url           TEXT,
    draft_doc_id  TEXT REFERENCES documents(id),
    owner         TEXT,
    note          TEXT,
    legacy_ref    TEXT,
    version       INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    CHECK (status <> 'scheduled' OR scheduled_for IS NOT NULL),
    CHECK (status <> 'published' OR published_at  IS NOT NULL)
);
CREATE INDEX ix_content_pieces_channel ON content_pieces(channel_id, status);
CREATE INDEX ix_content_pieces_status ON content_pieces(status, scheduled_for);
CREATE INDEX ix_content_pieces_product ON content_pieces(product_id) WHERE product_id IS NOT NULL;
CREATE INDEX ix_content_pieces_campaign ON content_pieces(campaign_id) WHERE campaign_id IS NOT NULL;
CREATE INDEX ix_content_pieces_published ON content_pieces(published_at) WHERE published_at IS NOT NULL;

-- ---------------------------------------------------------------------------
-- products: two columns added, nothing reshaped. The table keeps every row,
-- index and constraint 005 gave it.
--
-- current_release_id is a DECLARED pointer - what we say is current, which is
-- not always the newest released row (a rollback makes them differ, and that
-- difference is information). v_product_overview shows it beside the computed
-- newest release and never rewrites it. SQLite's foreign key guarantees the
-- release exists but cannot check it belongs to THIS product, so that
-- cross-row invariant is stated in v_biz_quality_issues.
-- ---------------------------------------------------------------------------
ALTER TABLE products ADD COLUMN current_release_id TEXT REFERENCES releases(id);
ALTER TABLE products ADD COLUMN launch_date TEXT
    CHECK (launch_date IS NULL OR launch_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]');
CREATE INDEX ix_products_release ON products(current_release_id) WHERE current_release_id IS NOT NULL;

-- ===========================================================================
-- Views. Read-time computation, all of it.
-- ===========================================================================

-- v_biz_quality_issues reads v_entity_index and is recreated at the end of
-- this file with the five new rules; it is dropped first so that the view it
-- depends on can be replaced.
DROP VIEW v_biz_quality_issues;

-- ---------------------------------------------------------------------------
-- v_entity_index: the same SQL mirror of Go's `entityTables` that 005 defined,
-- now carrying the four types this migration creates - 14 branches become 18.
--
-- This is the whole reason the dangling checks widen: they ask
-- "entity_type IN (SELECT entity_type FROM v_entity_types)" and then
-- look the id up here, so a context_edge, a doc_link or a task subject
-- pointing at a vanished content_piece is now reported without a single new
-- line of dangling logic.
-- ---------------------------------------------------------------------------
DROP VIEW v_entity_types;

-- ---------------------------------------------------------------------------
-- v_entity_types: the entity types this schema version knows how to resolve.
--
-- This MUST be a list of literals, never `SELECT DISTINCT entity_type FROM
-- v_entity_index`. v_entity_index draws its rows from the entity tables, so a
-- type whose table is empty simply does not appear there. The referential
-- guards below use this list to skip types they cannot resolve; deriving it
-- from data meant an empty table made its type "unknown", and a dangling
-- reference to it was silently NOT reported -- the check stopped checking at
-- exactly the moment a fresh instance most needed it. Adding a row here is a
-- schema fact, so it holds with zero rows in the table.
-- ---------------------------------------------------------------------------
CREATE VIEW v_entity_types AS
SELECT 'objective' AS entity_type
UNION ALL SELECT 'key_result'
UNION ALL SELECT 'initiative'
UNION ALL SELECT 'project'
UNION ALL SELECT 'milestone'
UNION ALL SELECT 'task'
UNION ALL SELECT 'account'
UNION ALL SELECT 'contact'
UNION ALL SELECT 'opportunity'
UNION ALL SELECT 'application'
UNION ALL SELECT 'contract'
UNION ALL SELECT 'ticket'
UNION ALL SELECT 'document'
UNION ALL SELECT 'product'
UNION ALL SELECT 'channel'
UNION ALL SELECT 'content_piece'
UNION ALL SELECT 'release'
UNION ALL SELECT 'campaign';

DROP VIEW v_entity_index;

CREATE VIEW v_entity_index AS
SELECT 'objective'   AS entity_type, id, name  AS title FROM objectives
UNION ALL SELECT 'key_result',    id, name          FROM key_results
UNION ALL SELECT 'initiative',    id, name          FROM initiatives
UNION ALL SELECT 'project',       id, name          FROM projects
UNION ALL SELECT 'milestone',     id, name          FROM milestones
UNION ALL SELECT 'task',          id, title         FROM tasks
UNION ALL SELECT 'account',       id, name          FROM accounts
UNION ALL SELECT 'contact',       id, name          FROM contacts
UNION ALL SELECT 'opportunity',   id, name          FROM opportunities
UNION ALL SELECT 'application',   id, name          FROM applications
UNION ALL SELECT 'contract',      id, name          FROM contracts
UNION ALL SELECT 'ticket',        id, title         FROM service_tickets
UNION ALL SELECT 'document',      id, title         FROM documents
UNION ALL SELECT 'product',       id, name          FROM products
UNION ALL SELECT 'channel',       id, name          FROM channels
UNION ALL SELECT 'content_piece', id, title         FROM content_pieces
UNION ALL SELECT 'release',       id, version_label FROM releases
UNION ALL SELECT 'campaign',      id, name          FROM campaigns;

-- ---------------------------------------------------------------------------
-- v_content_pipeline: the content board - how many pieces sit in each status,
-- what is due inside the next seven days, and what was scheduled for a moment
-- that has already passed and is still not published.
--
-- Grouped by channel and status, in the shape of v_pipeline (005). There is no
-- total row across channels for the same reason v_pipeline has none across
-- business lines: a 小红书 post and an offline talk are not one quantity.
-- Pieces with no channel yet group under a NULL channel_id, which is a real
-- and visible state rather than a silent exclusion.
-- ---------------------------------------------------------------------------
CREATE VIEW v_content_pipeline AS
SELECT c.channel_id,
       ch.name     AS channel_name,
       ch.platform AS channel_platform,
       c.status,
       (c.status NOT IN ('published','archived')) AS is_open,
       COUNT(*) AS piece_count,
       SUM(CASE WHEN c.scheduled_for IS NOT NULL
                 AND date(c.scheduled_for) BETWEEN (SELECT today    FROM v_clock)
                                               AND (SELECT week_end FROM v_clock)
                THEN 1 ELSE 0 END)                                     AS due_this_week_count,
       SUM(CASE WHEN c.status = 'scheduled' AND c.scheduled_for IS NOT NULL
                 AND c.scheduled_for < (SELECT now_utc FROM v_clock) || 'Z'
                THEN 1 ELSE 0 END)                                     AS overdue_unpublished_count,
       SUM(CASE WHEN c.draft_doc_id IS NULL THEN 1 ELSE 0 END)         AS without_draft_count,
       SUM(CASE WHEN c.product_id   IS NOT NULL THEN 1 ELSE 0 END)     AS with_product_count,
       SUM(CASE WHEN c.campaign_id  IS NOT NULL THEN 1 ELSE 0 END)     AS with_campaign_count,
       MIN(c.scheduled_for)                                            AS earliest_scheduled_for,
       MAX(c.published_at)                                             AS latest_published_at
  FROM content_pieces c
  LEFT JOIN channels ch ON ch.id = c.channel_id
 GROUP BY c.channel_id, ch.name, ch.platform, c.status;

-- ---------------------------------------------------------------------------
-- v_content_performance: one row per piece per metric - the latest reading,
-- and where that reading sits against the median of the same metric across the
-- other pieces on the same channel.
--
-- The median, not the mean: content is a long-tail distribution and one post
-- that went wide would drag an average until every other post looked like a
-- failure. SQLite has no MEDIAN(), so it is the average of the middle one or
-- two ranked values - for n=5 rank 3 twice, for n=4 ranks 2 and 3.
--
-- Comparison is within a channel only. 小红书 impressions and 公众号 reads are
-- different units of attention and ranking them together would be meaningless.
-- The channel join uses `IS` rather than `=` so that pieces with no channel
-- still form one comparable group instead of vanishing on a NULL comparison.
-- ---------------------------------------------------------------------------
CREATE VIEW v_content_performance AS
WITH latest_sample AS (
    SELECT subject_id AS content_piece_id, metric_name, unit, value, sampled_at, source
      FROM (SELECT m.subject_id, m.metric_name, m.unit, m.value, m.sampled_at, m.source,
                   ROW_NUMBER() OVER (PARTITION BY m.subject_id, m.metric_name
                                          ORDER BY m.sampled_at DESC, m.id DESC) AS rn
              FROM metric_samples m
             WHERE m.subject_type = 'content_piece')
     WHERE rn = 1
),
piece_metric AS (
    SELECT p.id AS content_piece_id, p.title, p.status, p.topic, p.url,
           p.channel_id, p.product_id, p.campaign_id, p.published_at, p.owner,
           l.metric_name, l.unit, l.value, l.sampled_at, l.source
      FROM content_pieces p
      JOIN latest_sample l ON l.content_piece_id = p.id
),
ranked AS (
    SELECT pm.channel_id, pm.metric_name, pm.value,
           ROW_NUMBER() OVER (PARTITION BY pm.channel_id, pm.metric_name ORDER BY pm.value) AS value_rank,
           COUNT(*)     OVER (PARTITION BY pm.channel_id, pm.metric_name)                   AS peer_count
      FROM piece_metric pm
),
channel_median AS (
    SELECT channel_id, metric_name, peer_count,
           AVG(value) AS median_value
      FROM ranked
     WHERE value_rank IN ((peer_count + 1) / 2, (peer_count + 2) / 2)
     GROUP BY channel_id, metric_name, peer_count
)
SELECT pm.content_piece_id, pm.title, pm.status, pm.topic, pm.url, pm.owner,
       pm.channel_id, ch.name AS channel_name, ch.platform AS channel_platform,
       pm.product_id, pr.name AS product_name,
       pm.campaign_id, ca.name AS campaign_name,
       pm.published_at,
       CASE WHEN pm.published_at IS NULL THEN NULL
            ELSE CAST(julianday((SELECT today FROM v_clock))
                    - julianday(date(pm.published_at)) AS INTEGER) END AS days_since_published,
       pm.metric_name, pm.unit, pm.value AS latest_value, pm.sampled_at AS latest_sampled_at,
       pm.source,
       cm.median_value        AS channel_median_value,
       cm.peer_count          AS channel_piece_count,
       pm.value - cm.median_value AS vs_median_delta,
       CASE WHEN cm.median_value IS NOT NULL AND cm.median_value <> 0
            THEN ROUND(pm.value / cm.median_value, 4) END AS vs_median_ratio,
       CASE WHEN cm.median_value IS NULL     THEN NULL
            WHEN pm.value > cm.median_value  THEN 'above'
            WHEN pm.value < cm.median_value  THEN 'below'
            ELSE 'at' END     AS vs_median
  FROM piece_metric pm
  LEFT JOIN channel_median cm ON cm.channel_id  IS pm.channel_id
                             AND cm.metric_name  =  pm.metric_name
  LEFT JOIN channels ch ON ch.id = pm.channel_id
  LEFT JOIN products pr ON pr.id = pm.product_id
  LEFT JOIN campaigns ca ON ca.id = pm.campaign_id;

-- ---------------------------------------------------------------------------
-- v_channel_growth: one row per channel per metric - the latest reading and
-- what it was 7 and 30 days ago, with the difference.
--
-- Every number is derived from metric_samples at read time; no follower count
-- is stored on channels and no delta is written back anywhere. "7 days ago"
-- means the newest sample taken on or before that date, because nobody records
-- a metric on exactly the right day, and a series with a gap must still be
-- comparable.
-- ---------------------------------------------------------------------------
CREATE VIEW v_channel_growth AS
WITH latest AS (
    SELECT subject_id AS channel_id, metric_name, unit, value, sampled_at
      FROM (SELECT m.subject_id, m.metric_name, m.unit, m.value, m.sampled_at,
                   ROW_NUMBER() OVER (PARTITION BY m.subject_id, m.metric_name
                                          ORDER BY m.sampled_at DESC, m.id DESC) AS rn
              FROM metric_samples m
             WHERE m.subject_type = 'channel')
     WHERE rn = 1
),
window_values AS (
    SELECT l.channel_id, l.metric_name, l.unit, l.value AS latest_value,
           l.sampled_at AS latest_sampled_at,
           (SELECT p.value FROM metric_samples p
             WHERE p.subject_type = 'channel' AND p.subject_id = l.channel_id
               AND p.metric_name = l.metric_name
               AND date(p.sampled_at) <= date((SELECT today FROM v_clock), '-7 day')
             ORDER BY p.sampled_at DESC, p.id DESC LIMIT 1)      AS value_7d_ago,
           (SELECT p.sampled_at FROM metric_samples p
             WHERE p.subject_type = 'channel' AND p.subject_id = l.channel_id
               AND p.metric_name = l.metric_name
               AND date(p.sampled_at) <= date((SELECT today FROM v_clock), '-7 day')
             ORDER BY p.sampled_at DESC, p.id DESC LIMIT 1)      AS sampled_at_7d_ago,
           (SELECT p.value FROM metric_samples p
             WHERE p.subject_type = 'channel' AND p.subject_id = l.channel_id
               AND p.metric_name = l.metric_name
               AND date(p.sampled_at) <= date((SELECT today FROM v_clock), '-30 day')
             ORDER BY p.sampled_at DESC, p.id DESC LIMIT 1)      AS value_30d_ago,
           (SELECT p.value FROM metric_samples p
             WHERE p.subject_type = 'channel' AND p.subject_id = l.channel_id
               AND p.metric_name = l.metric_name
             ORDER BY p.sampled_at ASC, p.id ASC LIMIT 1)        AS first_value,
           (SELECT p.sampled_at FROM metric_samples p
             WHERE p.subject_type = 'channel' AND p.subject_id = l.channel_id
               AND p.metric_name = l.metric_name
             ORDER BY p.sampled_at ASC, p.id ASC LIMIT 1)        AS first_sampled_at,
           (SELECT COUNT(*) FROM metric_samples p
             WHERE p.subject_type = 'channel' AND p.subject_id = l.channel_id
               AND p.metric_name = l.metric_name)                AS sample_count
      FROM latest l
)
SELECT ch.id AS channel_id, ch.name AS channel_name, ch.platform, ch.handle,
       ch.status, ch.owner,
       w.metric_name, w.unit,
       w.latest_value, w.latest_sampled_at,
       w.value_7d_ago, w.sampled_at_7d_ago, w.value_30d_ago,
       w.first_value, w.first_sampled_at, w.sample_count,
       w.latest_value - w.value_7d_ago  AS delta_7d,
       w.latest_value - w.value_30d_ago AS delta_30d,
       w.latest_value - w.first_value   AS delta_since_first,
       CASE WHEN w.value_7d_ago IS NOT NULL AND w.value_7d_ago <> 0
            THEN ROUND((w.latest_value - w.value_7d_ago) / ABS(w.value_7d_ago), 4) END  AS delta_7d_ratio,
       CASE WHEN w.value_30d_ago IS NOT NULL AND w.value_30d_ago <> 0
            THEN ROUND((w.latest_value - w.value_30d_ago) / ABS(w.value_30d_ago), 4) END AS delta_30d_ratio,
       CAST(julianday((SELECT today FROM v_clock))
          - julianday(date(w.latest_sampled_at)) AS INTEGER)     AS days_since_latest_sample,
       (SELECT COUNT(*) FROM content_pieces p
         WHERE p.channel_id = ch.id AND p.status = 'published')  AS published_piece_count
  FROM window_values w
  JOIN channels ch ON ch.id = w.channel_id;

-- ---------------------------------------------------------------------------
-- v_product_overview: one product on one row - the version we say is current,
-- the version that actually shipped last, what is being promoted right now,
-- the most recent measurement, and the contracts attached to it.
--
-- current_release_id is DECLARED and latest_released_* is COMPUTED, and both
-- are present with current_release_stale stating whether they disagree. The
-- declared pointer is never rewritten: after a rollback the newest released
-- row is deliberately not the current one, and a view that "corrected" that
-- would destroy the fact.
--
-- Contracts reach a product through context_edges, not a foreign key: 005
-- gave contracts no product_id, a contract can cover several products, and
-- inventing a column here would mean rebuilding contracts. Both edge
-- directions count, because "this contract sells that product" and "this
-- product is sold by that contract" are the same statement.
--
-- The money columns are contract money only. They are never added to a metric
-- value, and campaigns' budget is reported separately for the same reason.
-- ---------------------------------------------------------------------------
CREATE VIEW v_product_overview AS
WITH latest_release AS (
    SELECT product_id, id AS release_id, version_label, released_at
      FROM (SELECT r.product_id, r.id, r.version_label, r.released_at,
                   ROW_NUMBER() OVER (PARTITION BY r.product_id
                                          ORDER BY r.released_at DESC, r.id DESC) AS rn
              FROM releases r
             WHERE r.status = 'released')
     WHERE rn = 1
),
latest_metric AS (
    SELECT subject_id AS product_id, metric_name, unit, value, sampled_at
      FROM (SELECT m.subject_id, m.metric_name, m.unit, m.value, m.sampled_at,
                   ROW_NUMBER() OVER (PARTITION BY m.subject_id
                                          ORDER BY m.sampled_at DESC, m.id DESC) AS rn
              FROM metric_samples m
             WHERE m.subject_type = 'product')
     WHERE rn = 1
),
-- A soft edge in either direction links a contract to a product.
product_contract AS (
    SELECT e.to_id AS product_id, e.from_id AS contract_id
      FROM context_edges e
     WHERE e.from_type = 'contract' AND e.to_type = 'product'
    UNION
    SELECT e.from_id, e.to_id
      FROM context_edges e
     WHERE e.from_type = 'product' AND e.to_type = 'contract'
),
contract_rollup AS (
    SELECT pc.product_id,
           COUNT(*)                                                          AS contract_count,
           SUM(CASE WHEN k.status IN ('signed','active') THEN 1 ELSE 0 END)   AS active_contract_count,
           SUM(CASE WHEN k.status <> 'draft' THEN k.amount ELSE 0 END)        AS contract_amount_total,
           COUNT(DISTINCT k.currency)                                         AS contract_currency_count
      FROM product_contract pc
      JOIN contracts k ON k.id = pc.contract_id
     GROUP BY pc.product_id
)
SELECT p.id AS product_id, p.name, p.kind, p.status, p.positioning, p.repo_url,
       p.owner, p.launch_date,
       -- declared
       p.current_release_id,
       cr.version_label AS current_release_version,
       cr.status        AS current_release_status,
       cr.released_at   AS current_release_released_at,
       (cr.id IS NOT NULL AND cr.product_id <> p.id) AS current_release_wrong_product,
       -- computed
       lr.release_id      AS latest_released_id,
       lr.version_label   AS latest_released_version,
       lr.released_at     AS latest_released_at,
       (lr.release_id IS NOT NULL
        AND (p.current_release_id IS NULL OR p.current_release_id <> lr.release_id)) AS current_release_stale,
       (SELECT COUNT(*) FROM releases r WHERE r.product_id = p.id)            AS release_count,
       (SELECT COUNT(*) FROM releases r
         WHERE r.product_id = p.id AND r.status = 'released')                 AS released_count,
       (SELECT COUNT(*) FROM releases r
         WHERE r.product_id = p.id AND r.status IN ('planned','developing'))  AS upcoming_release_count,
       -- promotion in flight
       (SELECT COUNT(*) FROM campaigns c
         WHERE c.product_id = p.id AND c.status = 'running')                  AS running_campaign_count,
       (SELECT COALESCE(SUM(c.budget), 0) FROM campaigns c
         WHERE c.product_id = p.id AND c.status = 'running')                  AS running_campaign_budget,
       (SELECT MIN(c.end_date) FROM campaigns c
         WHERE c.product_id = p.id AND c.status = 'running')                  AS next_campaign_end_date,
       -- content pushing it
       (SELECT COUNT(*) FROM content_pieces cp WHERE cp.product_id = p.id)    AS content_piece_count,
       (SELECT COUNT(*) FROM content_pieces cp
         WHERE cp.product_id = p.id AND cp.status = 'published')              AS published_content_count,
       -- the newest measurement, whatever it measures; the full series is
       -- v_metric_trend and is deliberately not flattened into columns here
       lm.metric_name AS latest_metric_name,
       lm.value       AS latest_metric_value,
       lm.unit        AS latest_metric_unit,
       lm.sampled_at  AS latest_metric_at,
       (SELECT COUNT(DISTINCT m.metric_name) FROM metric_samples m
         WHERE m.subject_type = 'product' AND m.subject_id = p.id)            AS metric_name_count,
       -- contract money, and nothing but contract money
       COALESCE(k.contract_count, 0)          AS contract_count,
       COALESCE(k.active_contract_count, 0)   AS active_contract_count,
       COALESCE(k.contract_amount_total, 0)   AS contract_amount_total,
       COALESCE(k.contract_currency_count, 0) AS contract_currency_count,
       p.version, p.created_at, p.updated_at
  FROM products p
  LEFT JOIN releases        cr ON cr.id = p.current_release_id
  LEFT JOIN latest_release  lr ON lr.product_id = p.id
  LEFT JOIN latest_metric   lm ON lm.product_id = p.id
  LEFT JOIN contract_rollup k  ON k.product_id  = p.id;

-- ---------------------------------------------------------------------------
-- v_campaign_effect: what the numbers did during a campaign's window, printed
-- beside what the campaign cost.
--
-- It states a baseline, a first and a last reading inside the window, and the
-- arithmetic difference. It does NOT say the campaign caused the difference,
-- and there is no column here that could be read as saying so - no "lift", no
-- "roi", no attribution. Seasonality, a release, a post going wide and the
-- campaign are all inside that window and this view cannot tell them apart.
--
-- Two subjects are measured: the campaign itself
-- (metric_samples subject_type='campaign' - signups at the meetup, leaflets
-- handed out) and its product (downloads, DAU). budget rides along as a plain
-- column and is never summed with a metric value.
--
-- A campaign with no end_date is still running, so the window ends today.
-- ---------------------------------------------------------------------------
CREATE VIEW v_campaign_effect AS
WITH win AS (
    SELECT c.id AS campaign_id, c.name AS campaign_name, c.status, c.channel_type,
           c.budget, c.owner, c.product_id, c.start_date, c.end_date,
           COALESCE(c.end_date, (SELECT today FROM v_clock)) AS window_end
      FROM campaigns c
     WHERE c.start_date IS NOT NULL
),
subject AS (
    SELECT campaign_id, 'campaign' AS subject_type, campaign_id AS subject_id FROM win
    UNION ALL
    SELECT campaign_id, 'product', product_id FROM win WHERE product_id IS NOT NULL
),
pair AS (
    SELECT DISTINCT s.campaign_id, s.subject_type, s.subject_id, m.metric_name
      FROM subject s
      JOIN metric_samples m ON m.subject_type = s.subject_type
                           AND m.subject_id   = s.subject_id
)
SELECT w.campaign_id, w.campaign_name, w.status AS campaign_status, w.channel_type,
       w.owner, w.budget, w.start_date, w.end_date, w.window_end,
       w.product_id, pr.name AS product_name,
       p.subject_type, p.subject_id, p.metric_name,
       (SELECT m.unit FROM metric_samples m
         WHERE m.subject_type = p.subject_type AND m.subject_id = p.subject_id
           AND m.metric_name = p.metric_name
         ORDER BY m.sampled_at DESC, m.id DESC LIMIT 1)            AS unit,
       -- the last reading BEFORE the window opened
       (SELECT m.value FROM metric_samples m
         WHERE m.subject_type = p.subject_type AND m.subject_id = p.subject_id
           AND m.metric_name = p.metric_name
           AND date(m.sampled_at) < w.start_date
         ORDER BY m.sampled_at DESC, m.id DESC LIMIT 1)            AS baseline_value,
       (SELECT m.sampled_at FROM metric_samples m
         WHERE m.subject_type = p.subject_type AND m.subject_id = p.subject_id
           AND m.metric_name = p.metric_name
           AND date(m.sampled_at) < w.start_date
         ORDER BY m.sampled_at DESC, m.id DESC LIMIT 1)            AS baseline_sampled_at,
       -- first and last reading INSIDE the window
       (SELECT m.value FROM metric_samples m
         WHERE m.subject_type = p.subject_type AND m.subject_id = p.subject_id
           AND m.metric_name = p.metric_name
           AND date(m.sampled_at) BETWEEN w.start_date AND w.window_end
         ORDER BY m.sampled_at ASC, m.id ASC LIMIT 1)              AS window_first_value,
       (SELECT m.value FROM metric_samples m
         WHERE m.subject_type = p.subject_type AND m.subject_id = p.subject_id
           AND m.metric_name = p.metric_name
           AND date(m.sampled_at) BETWEEN w.start_date AND w.window_end
         ORDER BY m.sampled_at DESC, m.id DESC LIMIT 1)            AS window_last_value,
       (SELECT m.sampled_at FROM metric_samples m
         WHERE m.subject_type = p.subject_type AND m.subject_id = p.subject_id
           AND m.metric_name = p.metric_name
           AND date(m.sampled_at) BETWEEN w.start_date AND w.window_end
         ORDER BY m.sampled_at DESC, m.id DESC LIMIT 1)            AS window_last_sampled_at,
       (SELECT COUNT(*) FROM metric_samples m
         WHERE m.subject_type = p.subject_type AND m.subject_id = p.subject_id
           AND m.metric_name = p.metric_name
           AND date(m.sampled_at) BETWEEN w.start_date AND w.window_end) AS window_sample_count,
       CAST(julianday(w.window_end) - julianday(w.start_date) AS INTEGER) AS window_days
  FROM win w
  JOIN pair p ON p.campaign_id = w.campaign_id
  LEFT JOIN products pr ON pr.id = w.product_id;

-- ---------------------------------------------------------------------------
-- v_biz_quality_issues: recreated from 005 unchanged, with six rules appended
-- and one thing that needed no code at all.
--
-- The three dangling checks at the bottom are byte-for-byte what 005 wrote.
-- They now cover channel, content_piece, release and campaign because
-- v_entity_index above knows those four types - which is exactly why that view
-- exists instead of a hand-written list per check.
--
-- Every row still only STATES. Nothing here blocks a write and nothing here
-- repairs a number.
-- ---------------------------------------------------------------------------
CREATE VIEW v_biz_quality_issues AS
-- A deal nobody has touched, sitting in the same stage.
SELECT 'opportunity' AS entity_type, f.opportunity_id AS entity_id, f.name AS title,
       'opportunity_stalled' AS issue,
       'open opportunity has not changed stage or been contacted within the stale window' AS detail
  FROM v_opportunity_full f
 WHERE f.is_open
   AND f.days_in_stage > (SELECT CAST(value AS INTEGER) FROM ops_settings
                           WHERE key = 'biz_opportunity_stale_days')
   AND (f.last_interaction_at IS NULL
        OR f.days_since_interaction > (SELECT CAST(value AS INTEGER) FROM ops_settings
                                        WHERE key = 'biz_opportunity_stale_days'))
UNION ALL
SELECT 'opportunity', o.id, o.name, 'won_opportunity_without_contract',
       'opportunity is won but no contract records what was agreed'
  FROM opportunities o
 WHERE o.stage = 'won'
   AND NOT EXISTS (SELECT 1 FROM contracts k WHERE k.opportunity_id = o.id)
UNION ALL
SELECT 'opportunity', o.id, o.name, 'open_opportunity_without_next_step',
       'open opportunity records no next step'
  FROM opportunities o
 WHERE o.stage NOT IN ('won','lost')
   AND (o.next_step IS NULL OR o.next_step = '')
UNION ALL
-- Signed work with nowhere to execute it.
SELECT 'contract', k.id, k.name, 'contract_without_delivery_project',
       'contract is signed or active but no project delivers it'
  FROM contracts k
 WHERE k.status IN ('signed','active')
   AND k.kind IN ('sales','piecework')
   AND NOT EXISTS (SELECT 1 FROM projects p WHERE p.contract_id = k.id)
UNION ALL
-- The headline rule: the declared amount and the receivable plan disagree.
-- Stated, never repaired. contracts.amount stays exactly as it was written.
SELECT 'contract', cr.contract_id, cr.name, 'contract_amount_plan_mismatch',
       'contract amount and the sum of its receivable plans disagree'
  FROM v_contract_receivable cr
 WHERE cr.plan_mismatch
UNION ALL
SELECT 'contract', cr.contract_id, cr.name, 'receipts_exceed_contract_amount',
       'receipts recorded against this contract exceed the contract amount'
  FROM v_contract_receivable cr
 WHERE cr.over_received
UNION ALL
SELECT 'contract', cr.contract_id, cr.name, 'piecework_line_mismatch',
       'unit price times quantity does not equal the contract amount'
  FROM v_contract_receivable cr
 WHERE cr.line_mismatch
UNION ALL
SELECT 'contract', k.id, k.name, 'signed_contract_without_plan',
       'contract is signed or active but has no receivable plan at all'
  FROM contracts k
 WHERE k.status IN ('signed','active')
   AND NOT EXISTS (SELECT 1 FROM receivable_plans rp WHERE rp.contract_id = k.id)
UNION ALL
SELECT 'contract', ag.contract_id, ag.contract_name, 'receivable_plan_overdue',
       'a receivable instalment is past its due date and not marked received'
  FROM v_receivable_overdue ag
UNION ALL
-- Mode D: the announced prize and the receivable prize are separate facts.
SELECT 'application', ap.id, ap.name, 'won_application_without_prize_contract',
       'application is won but no contract records the prize or the award'
  FROM applications ap
 WHERE ap.stage = 'won'
   AND NOT EXISTS (SELECT 1 FROM contracts k WHERE k.application_id = ap.id)
UNION ALL
SELECT 'application', ap.id, ap.name, 'prize_amount_contract_mismatch',
       'announced prize amount and the contracted amount disagree'
  FROM applications ap
 WHERE ap.prize_amount IS NOT NULL
   AND EXISTS (SELECT 1 FROM contracts k WHERE k.application_id = ap.id)
   AND ABS(ap.prize_amount
           - (SELECT SUM(k.amount) FROM contracts k WHERE k.application_id = ap.id)) > 0.005
UNION ALL
SELECT 'application', ap.id, ap.name, 'application_submitted_without_result',
       'application has been submitted past the stale window with no decision and no next step'
  FROM applications ap
 WHERE ap.stage IN ('submitted','under_review')
   AND ap.submitted_at IS NOT NULL
   AND julianday((SELECT today FROM v_clock)) - julianday(date(ap.submitted_at))
       > (SELECT CAST(value AS INTEGER) FROM ops_settings WHERE key = 'biz_application_stale_days')
   AND (ap.next_step IS NULL OR ap.next_step = '')
UNION ALL
-- Relationship hygiene.
SELECT 'contact', c.id, c.name, 'contact_without_channel',
       'active contact has no phone, email or wechat, so there is no way to reach them'
  FROM contacts c
 WHERE c.status = 'active'
   AND COALESCE(c.phone, '')  = ''
   AND COALESCE(c.email, '')  = ''
   AND COALESCE(c.wechat, '') = ''
UNION ALL
SELECT 'account', a.id, a.name, 'account_without_dossier',
       'customer account has no dossier document'
  FROM accounts a
 WHERE a.account_type = 'customer' AND a.status = 'active'
   AND NOT EXISTS (SELECT 1 FROM doc_links l
                    WHERE l.entity_type = 'account' AND l.entity_id = a.id
                      AND l.link_type = 'dossier')
UNION ALL
SELECT 'project', vb.project_id, vb.project_name, 'account_link_conflict',
       'project names one account while its contract names a different one'
  FROM v_project_business vb
 WHERE vb.account_link_conflict
UNION ALL
SELECT 'project', vb.project_id, vb.project_name, 'delivered_with_open_tickets',
       'delivery project is finished but service tickets on it are still open'
  FROM v_project_business vb
 WHERE vb.status IN ('done','archived') AND vb.open_ticket_count > 0
UNION ALL
SELECT 'document', d.document_id, d.title, 'document_review_overdue',
       'document is past its re-evaluation date and has not been superseded'
  FROM v_doc_review_due d
UNION ALL
SELECT 'document', d.id, d.title, 'document_without_file',
       'document has no file on disk at all'
  FROM documents d
 WHERE NOT EXISTS (SELECT 1 FROM document_files f WHERE f.doc_id = d.id)
UNION ALL
-- ---- mode B and C, new in 006 -------------------------------------------
-- The publishing slot came and went.
SELECT 'content_piece', c.id, c.title, 'content_scheduled_not_published',
       'content piece is past the moment it was scheduled for and is still not published'
  FROM content_pieces c
 WHERE c.status = 'scheduled'
   AND c.scheduled_for IS NOT NULL
   AND c.scheduled_for < (SELECT now_utc FROM v_clock) || 'Z'
UNION ALL
-- Published and never measured: the post exists, the result is unknown.
SELECT 'content_piece', c.id, c.title, 'published_without_metrics',
       'content piece was published past the measurement window and no metric has been recorded for it'
  FROM content_pieces c
 WHERE c.status = 'published'
   AND c.published_at IS NOT NULL
   AND julianday((SELECT today FROM v_clock)) - julianday(date(c.published_at))
       > (SELECT CAST(value AS INTEGER) FROM ops_settings WHERE key = 'biz_content_metric_days')
   AND NOT EXISTS (SELECT 1 FROM metric_samples m
                    WHERE m.subject_type = 'content_piece' AND m.subject_id = c.id)
UNION ALL
-- An idea may legitimately not know where it will go yet; this states the gap
-- rather than a CHECK refusing to record the idea at all.
SELECT 'content_piece', c.id, c.title, 'content_without_channel',
       'content piece names no channel, so there is nowhere for it to be published'
  FROM content_pieces c
 WHERE c.channel_id IS NULL
   AND c.status <> 'archived'
UNION ALL
-- The product says it shipped; nothing records what shipped.
SELECT 'product', p.id, p.name, 'released_product_without_release',
       'product status is released but no release row records a version'
  FROM products p
 WHERE p.status = 'released'
   AND NOT EXISTS (SELECT 1 FROM releases r WHERE r.product_id = p.id)
UNION ALL
-- SQLite's foreign key proves the release exists; only this can prove it
-- belongs to this product.
SELECT 'product', v.product_id, v.name, 'current_release_wrong_product',
       'product points at a current release that belongs to a different product'
  FROM v_product_overview v
 WHERE v.current_release_wrong_product
UNION ALL
-- The push is over and nobody wrote down what happened.
SELECT 'campaign', c.id, c.name, 'campaign_ended_without_metrics',
       'campaign ended past the measurement window with no effect metric recorded'
  FROM campaigns c
 WHERE c.status = 'ended'
   AND c.end_date IS NOT NULL
   AND julianday((SELECT today FROM v_clock)) - julianday(c.end_date)
       > (SELECT CAST(value AS INTEGER) FROM ops_settings WHERE key = 'biz_campaign_metric_days')
   AND NOT EXISTS (SELECT 1 FROM metric_samples m
                    WHERE m.subject_type = 'campaign' AND m.subject_id = c.id)
UNION ALL
-- Dangling soft edges and links. Only ends whose type v_entity_index knows are
-- checked; a type it does not yet carry is left to `doctor` rather than
-- reported as a false positive. Unchanged from 005 - the widened
-- v_entity_index is what extends these to content and product.
SELECT 'context_edge', e.id, e.from_type || ':' || e.from_id || ' -> ' || e.to_type || ':' || e.to_id,
       'dangling_context_edge',
       'one end of this soft relation no longer exists'
  FROM context_edges e
 WHERE (e.from_type IN (SELECT entity_type FROM v_entity_types)
        AND NOT EXISTS (SELECT 1 FROM v_entity_index x
                         WHERE x.entity_type = e.from_type AND x.id = e.from_id))
    OR (e.to_type IN (SELECT entity_type FROM v_entity_types)
        AND NOT EXISTS (SELECT 1 FROM v_entity_index x
                         WHERE x.entity_type = e.to_type AND x.id = e.to_id))
UNION ALL
SELECT 'doc_link', l.id, l.entity_type || ':' || l.entity_id, 'dangling_doc_link',
       'document is linked to an object that no longer exists'
  FROM doc_links l
 WHERE l.entity_type IN (SELECT entity_type FROM v_entity_types)
   AND NOT EXISTS (SELECT 1 FROM v_entity_index x
                    WHERE x.entity_type = l.entity_type AND x.id = l.entity_id)
UNION ALL
SELECT 'task', t.id, t.title, 'dangling_task_subject',
       'task points at a business subject that no longer exists'
  FROM tasks t
 WHERE t.subject_type IS NOT NULL
   AND t.subject_type IN (SELECT entity_type FROM v_entity_types)
   AND NOT EXISTS (SELECT 1 FROM v_entity_index x
                    WHERE x.entity_type = t.subject_type AND x.id = t.subject_id);
