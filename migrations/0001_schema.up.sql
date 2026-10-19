-- 0001_schema — схема MVP целиком.
--
-- Одна миграция вместо истории: репозиторий начинается с MVP, и прошлые
-- шаги схемы ему не нужны. Дальше — как обычно: применённую миграцию не
-- редактируют, изменения идут новой парой .up/.down.
--
-- Модули и их схемы (правило «модуль читает и пишет только свою схему»
-- проверяет internal/arch):
--   iam        — организации, пользователи, сессии, роли и права, ученики;
--   classroom  — классы и состав;
--   content    — уроки, версии, упражнения, настройка разговора с аватаром;
--   learning   — назначения, выдачи, попытки, ответы, реплики разговора;
--   ops        — учёт вызовов моделей и журнал голосовых соединений;
--   lexicon    — справочник языков и уровней.
--
-- Таблицы событий (learning.answer_events, learning.dialog_turns,
-- ops.pipeline_calls) секционированы по неделям; секции вперёд создаёт
-- public.ensure_weekly_partitions — здесь и раз в сутки из приложения.

CREATE SCHEMA classroom;

CREATE SCHEMA content;

CREATE SCHEMA iam;

CREATE SCHEMA learning;

CREATE SCHEMA lexicon;

CREATE SCHEMA ops;

CREATE FUNCTION content.guard_published_version() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF OLD.status IN ('published', 'archived') THEN
    IF OLD.status = 'published' AND NEW.status = 'archived'
       AND (to_jsonb(NEW) - 'status' - 'updated_at')
           IS NOT DISTINCT FROM (to_jsonb(OLD) - 'status' - 'updated_at') THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'Версия урока % опубликована или архивирована и неизменяема', OLD.id
      USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION ops.live_events_append_only() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  RAISE EXCEPTION 'Журнал ops.live_events доступен только для добавления'
    USING ERRCODE = 'restrict_violation';
END;
$$;

CREATE FUNCTION public.ensure_weekly_partitions(p_schema text, p_table text, p_from date, p_weeks integer) RETURNS integer
    LANGUAGE plpgsql
    AS $$
DECLARE
  v_week_start date := date_trunc('week', p_from)::date;
  v_week_end   date;
  v_name       text;
  v_created    int := 0;
  i            int;
BEGIN
  IF p_weeks < 1 THEN
    RAISE EXCEPTION 'ensure_weekly_partitions: p_weeks must be >= 1, got %', p_weeks;
  END IF;

  FOR i IN 0 .. p_weeks - 1 LOOP
    v_week_end := v_week_start + 7;
    -- IYYY/IW — ISO-год и ISO-неделя: у границы года не даёт коллизии имён.
    v_name := format('%s_%s', p_table, to_char(v_week_start, 'IYYY"w"IW'));

    IF to_regclass(format('%I.%I', p_schema, v_name)) IS NULL THEN
      EXECUTE format(
        'CREATE TABLE %I.%I PARTITION OF %I.%I FOR VALUES FROM (%L) TO (%L)',
        p_schema, v_name, p_schema, p_table, v_week_start, v_week_end
      );
      v_created := v_created + 1;
    END IF;

    v_week_start := v_week_end;
  END LOOP;

  RETURN v_created;
END;
$$;

CREATE FUNCTION public.touch_updated_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END;
$$;

CREATE TABLE classroom.classes (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    teacher_id uuid NOT NULL,
    title text NOT NULL,
    subject text DEFAULT ''::text NOT NULL,
    lang_code text NOT NULL,
    default_level text NOT NULL,
    join_code text NOT NULL,
    join_open boolean DEFAULT true NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT classes_default_level_check CHECK ((default_level = ANY (ARRAY['A1'::text, 'A2'::text, 'B1'::text, 'B2'::text, 'C1'::text]))),
    CONSTRAINT classes_status_check CHECK ((status = ANY (ARRAY['active'::text, 'archived'::text])))
);

CREATE TABLE classroom.enrollments (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    class_id uuid NOT NULL,
    student_id uuid NOT NULL,
    teacher_id uuid NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    joined_at timestamp with time zone DEFAULT now() NOT NULL,
    removed_at timestamp with time zone,
    last_seen_at timestamp with time zone,
    CONSTRAINT enrollments_status_check CHECK ((status = ANY (ARRAY['active'::text, 'removed'::text])))
);

CREATE TABLE content.avatar_configs (
    lesson_version_id uuid NOT NULL,
    org_id uuid NOT NULL,
    role_prompt text DEFAULT ''::text NOT NULL,
    max_turns integer DEFAULT 8 NOT NULL,
    hint_enabled boolean DEFAULT false NOT NULL,
    dictionary_enabled boolean DEFAULT false NOT NULL,
    voice_profile_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    practice_phrases text[] DEFAULT '{}'::text[] NOT NULL,
    practice_grammar text[] DEFAULT '{}'::text[] NOT NULL,
    configured boolean DEFAULT false NOT NULL,
    practice_phrase_notes text[] DEFAULT '{}'::text[] NOT NULL,
    practice_grammar_notes text[] DEFAULT '{}'::text[] NOT NULL,
    CONSTRAINT avatar_configs_grammar_len CHECK ((cardinality(practice_grammar) <= 10)),
    CONSTRAINT avatar_configs_max_turns_check CHECK (((max_turns >= 3) AND (max_turns <= 20))),
    CONSTRAINT avatar_configs_phrases_len CHECK ((cardinality(practice_phrases) <= 30)),
    CONSTRAINT avatar_configs_role_len CHECK ((char_length(role_prompt) <= 1000))
);

CREATE TABLE content.exercises (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    lesson_version_id uuid NOT NULL,
    type text NOT NULL,
    "position" integer NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    instruction text DEFAULT ''::text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    answer_key jsonb DEFAULT '{}'::jsonb NOT NULL,
    raw_source text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT exercises_type_check CHECK ((type = ANY (ARRAY['matching'::text, 'gap_fill'::text, 'logical'::text, 'grammar'::text, 'custom'::text])))
);

CREATE TABLE content.grammar_topics (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    lesson_version_id uuid NOT NULL,
    "position" integer NOT NULL,
    title text NOT NULL,
    example text DEFAULT ''::text NOT NULL,
    origin text DEFAULT 'model'::text NOT NULL,
    topic_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    explanation text DEFAULT ''::text NOT NULL,
    examples jsonb DEFAULT '[]'::jsonb NOT NULL,
    practice jsonb DEFAULT '[]'::jsonb NOT NULL,
    CONSTRAINT grammar_topics_origin_check CHECK ((origin = ANY (ARRAY['model'::text, 'teacher'::text])))
);

CREATE TABLE content.learning_objectives (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    lesson_version_id uuid NOT NULL,
    "position" integer NOT NULL,
    text text NOT NULL
);

CREATE TABLE content.lesson_versions (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    lesson_id uuid NOT NULL,
    version integer NOT NULL,
    status text DEFAULT 'draft'::text NOT NULL,
    title text NOT NULL,
    level text NOT NULL,
    refined_text text DEFAULT ''::text NOT NULL,
    frank_text text DEFAULT ''::text NOT NULL,
    grammar_raw text DEFAULT ''::text NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    published_at timestamp with time zone,
    created_by uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    quality_warnings jsonb DEFAULT '[]'::jsonb NOT NULL,
    CONSTRAINT lesson_versions_level_check CHECK ((level = ANY (ARRAY['A1'::text, 'A2'::text, 'B1'::text, 'B2'::text, 'C1'::text]))),
    CONSTRAINT lesson_versions_quality_warnings_check CHECK ((jsonb_typeof(quality_warnings) = 'array'::text)),
    CONSTRAINT lesson_versions_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'ready'::text, 'published'::text, 'archived'::text]))),
    CONSTRAINT lesson_versions_version_check CHECK ((version >= 1))
);

CREATE TABLE content.lessons (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    author_id uuid NOT NULL,
    title text NOT NULL,
    lang_code text NOT NULL,
    level text NOT NULL,
    status text DEFAULT 'draft'::text NOT NULL,
    source_kind text DEFAULT 'text'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    source_material_id uuid,
    CONSTRAINT lessons_level_check CHECK ((level = ANY (ARRAY['A1'::text, 'A2'::text, 'B1'::text, 'B2'::text, 'C1'::text]))),
    CONSTRAINT lessons_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'published'::text, 'archived'::text])))
);

CREATE TABLE content.lexical_units (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    lesson_version_id uuid NOT NULL,
    "position" integer NOT NULL,
    phrase text NOT NULL,
    translation text DEFAULT ''::text NOT NULL,
    origin text DEFAULT 'model'::text NOT NULL,
    lexeme_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT lexical_units_origin_check CHECK ((origin = ANY (ARRAY['model'::text, 'teacher'::text])))
);

CREATE TABLE iam.email_tokens (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT email_tokens_kind_check CHECK ((kind = ANY (ARRAY['verify'::text, 'reset'::text, 'approve'::text])))
);

CREATE TABLE iam.login_attempts (
    id bigint NOT NULL,
    email_normalized text NOT NULL,
    ip inet,
    succeeded boolean NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE iam.login_attempts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE iam.login_attempts_id_seq OWNED BY iam.login_attempts.id;

CREATE TABLE iam.organizations (
    id uuid NOT NULL,
    kind text NOT NULL,
    title text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT organizations_kind_check CHECK ((kind = ANY (ARRAY['b2c'::text, 'school'::text, 'partner'::text]))),
    CONSTRAINT organizations_status_check CHECK ((status = ANY (ARRAY['active'::text, 'archived'::text])))
);

CREATE TABLE iam.permissions (
    code text NOT NULL,
    title_ru text NOT NULL
);

CREATE TABLE iam.role_permissions (
    role_code text NOT NULL,
    permission_code text NOT NULL
);

CREATE TABLE iam.roles (
    code text NOT NULL,
    title_ru text NOT NULL,
    description text DEFAULT ''::text NOT NULL
);

CREATE TABLE iam.sessions (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    user_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    impersonated_by uuid,
    impersonate_reason text,
    read_only boolean DEFAULT false NOT NULL,
    user_agent text DEFAULT ''::text NOT NULL,
    ip inet,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL
);

CREATE TABLE iam.student_tokens (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    student_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    device_label text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL
);

CREATE TABLE iam.students (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    display_name text NOT NULL,
    public_tag text NOT NULL,
    public_code text NOT NULL,
    level text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone,
    CONSTRAINT students_level_check CHECK (((level IS NULL) OR (level = ANY (ARRAY['A1'::text, 'A2'::text, 'B1'::text, 'B2'::text, 'C1'::text]))))
);

CREATE TABLE iam.users (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    email text NOT NULL,
    email_normalized text NOT NULL,
    password_hash text NOT NULL,
    display_name text NOT NULL,
    role_code text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    origin text DEFAULT 'self'::text NOT NULL,
    email_verified_at timestamp with time zone,
    last_login_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    approved_at timestamp with time zone,
    approved_by uuid,
    CONSTRAINT users_origin_check CHECK ((origin = ANY (ARRAY['self'::text, 'demo'::text, 'admin'::text]))),
    CONSTRAINT users_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'awaiting_approval'::text, 'active'::text, 'blocked'::text, 'deleted'::text])))
);

CREATE TABLE learning.answer_events (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    attempt_id uuid NOT NULL,
    student_id uuid NOT NULL,
    exercise_id uuid,
    step integer DEFAULT 0 NOT NULL,
    answer text DEFAULT ''::text NOT NULL,
    is_correct boolean NOT NULL,
    score_delta integer DEFAULT 0 NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    exercise_kind text DEFAULT ''::text NOT NULL
)
PARTITION BY RANGE (occurred_at);

CREATE TABLE learning.assignments (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    lesson_version_id uuid NOT NULL,
    class_id uuid NOT NULL,
    target_type text DEFAULT 'class'::text NOT NULL,
    target_id uuid,
    opens_at timestamp with time zone,
    closes_at timestamp with time zone,
    state text DEFAULT 'scheduled'::text NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    closed_at timestamp with time zone,
    CONSTRAINT assignments_state_check CHECK ((state = ANY (ARRAY['scheduled'::text, 'active'::text, 'closed'::text]))),
    CONSTRAINT assignments_target_type_check CHECK ((target_type = ANY (ARRAY['class'::text, 'subgroup'::text, 'student'::text]))),
    CONSTRAINT assignments_window_ck CHECK (((opens_at IS NULL) OR (closes_at IS NULL) OR (closes_at > opens_at)))
);

CREATE TABLE learning.attempt_feedback (
    attempt_id uuid NOT NULL,
    org_id uuid NOT NULL,
    student_id uuid NOT NULL,
    lesson text,
    avatar text,
    lesson_why text DEFAULT ''::text NOT NULL,
    avatar_why text DEFAULT ''::text NOT NULL,
    unclear text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT attempt_feedback_avatar_check CHECK ((avatar = ANY (ARRAY['bad'::text, 'ok'::text, 'good'::text]))),
    CONSTRAINT attempt_feedback_lesson_check CHECK ((lesson = ANY (ARRAY['bad'::text, 'ok'::text, 'good'::text])))
);

CREATE TABLE learning.attempts (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    assignment_id uuid NOT NULL,
    student_id uuid NOT NULL,
    current_step integer DEFAULT 0 NOT NULL,
    score integer DEFAULT 0 NOT NULL,
    correct_answers integer DEFAULT 0 NOT NULL,
    total_answers integer DEFAULT 0 NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    avatar_state jsonb,
    avatar_turns integer DEFAULT 0 NOT NULL,
    avatar_sessions integer DEFAULT 0 NOT NULL,
    avatar_feedback text DEFAULT ''::text NOT NULL,
    avatar_feedback_turns integer DEFAULT '-1'::integer NOT NULL,
    total_questions integer DEFAULT 0 NOT NULL,
    answers jsonb DEFAULT '{}'::jsonb NOT NULL,
    completed boolean DEFAULT false NOT NULL,
    avatar_engine jsonb,
    avatar_closed_at timestamp with time zone,
    avatar_report jsonb,
    avatar_report_status text DEFAULT ''::text NOT NULL,
    avatar_report_started_at timestamp with time zone,
    avatar_incidents jsonb DEFAULT '[]'::jsonb NOT NULL,
    avatar_lease uuid,
    avatar_lease_until timestamp with time zone,
    avatar_responses jsonb DEFAULT '{}'::jsonb NOT NULL,
    avatar_phrases_credited integer,
    avatar_phrases_total integer,
    CONSTRAINT attempts_avatar_report_status_check CHECK ((avatar_report_status = ANY (ARRAY[''::text, 'pending'::text, 'ready'::text, 'failed'::text]))),
    CONSTRAINT attempts_avatar_sessions_check CHECK ((avatar_sessions >= 0)),
    CONSTRAINT attempts_avatar_turns_check CHECK ((avatar_turns >= 0)),
    CONSTRAINT attempts_total_questions_check CHECK ((total_questions >= 0))
);

CREATE TABLE learning.dialog_turns (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    attempt_id uuid NOT NULL,
    student_id uuid NOT NULL,
    role text NOT NULL,
    content text NOT NULL,
    client_msg_id text,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    turn_no integer,
    input text DEFAULT 'text'::text NOT NULL,
    asr_text text,
    asr_confidence real,
    edited boolean DEFAULT false NOT NULL,
    translation text,
    steer text,
    steer_turn integer,
    CONSTRAINT dialog_turns_input_check CHECK ((input = ANY (ARRAY['text'::text, 'voice'::text]))),
    CONSTRAINT dialog_turns_role_check CHECK ((role = ANY (ARRAY['student'::text, 'avatar'::text]))),
    CONSTRAINT dialog_turns_steer_turn_check CHECK ((steer_turn >= 0))
)
PARTITION BY RANGE (occurred_at);

CREATE TABLE learning.issue_tasks (
    org_id uuid NOT NULL,
    assignment_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    task_key text NOT NULL,
    CONSTRAINT issue_tasks_task_key_check CHECK ((task_key <> ''::text))
);

CREATE TABLE learning.issues (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    assignment_id uuid NOT NULL,
    kind text NOT NULL,
    opened_at timestamp with time zone DEFAULT now() NOT NULL,
    due_at timestamp with time zone,
    closed_at timestamp with time zone,
    legacy boolean DEFAULT false NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT issues_kind_check CHECK ((kind = ANY (ARRAY['class'::text, 'home'::text])))
);

CREATE TABLE learning.presence (
    org_id uuid NOT NULL,
    student_id uuid NOT NULL,
    seen_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE learning.task_progress (
    org_id uuid NOT NULL,
    attempt_id uuid NOT NULL,
    task_key text NOT NULL,
    issue_id uuid,
    opened_at timestamp with time zone DEFAULT now() NOT NULL,
    done_at timestamp with time zone,
    CONSTRAINT task_progress_task_key_check CHECK ((task_key <> ''::text))
);

CREATE TABLE lexicon.languages (
    code text NOT NULL,
    title_ru text NOT NULL,
    title_native text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE lexicon.levels (
    code text NOT NULL,
    "position" smallint NOT NULL,
    title_ru text NOT NULL
);

CREATE TABLE ops.live_events (
    id uuid NOT NULL,
    org_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    actor_role text NOT NULL,
    stage text NOT NULL,
    result text NOT NULL,
    protocol text DEFAULT ''::text NOT NULL,
    network text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT live_events_result_check CHECK ((result = ANY (ARRAY['ok'::text, 'failed'::text])))
);

CREATE TABLE ops.pipeline_calls (
    id uuid NOT NULL,
    org_id uuid,
    user_id uuid,
    pipeline text NOT NULL,
    stage text DEFAULT ''::text NOT NULL,
    pool text DEFAULT ''::text NOT NULL,
    provider text DEFAULT ''::text NOT NULL,
    model text DEFAULT ''::text NOT NULL,
    tokens_in integer DEFAULT 0 NOT NULL,
    tokens_out integer DEFAULT 0 NOT NULL,
    queue_ms integer DEFAULT 0 NOT NULL,
    duration_ms integer DEFAULT 0 NOT NULL,
    cost_micro bigint DEFAULT 0 NOT NULL,
    ok boolean DEFAULT true NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    ref_id uuid,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    quality_metrics jsonb DEFAULT '{}'::jsonb NOT NULL,
    billing jsonb DEFAULT '[]'::jsonb NOT NULL,
    CONSTRAINT pipeline_calls_quality_metrics_check CHECK ((jsonb_typeof(quality_metrics) = 'object'::text))
)
PARTITION BY RANGE (started_at);

ALTER TABLE ONLY iam.login_attempts ALTER COLUMN id SET DEFAULT nextval('iam.login_attempts_id_seq'::regclass);

ALTER TABLE ONLY classroom.classes
    ADD CONSTRAINT classes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY classroom.enrollments
    ADD CONSTRAINT enrollments_pkey PRIMARY KEY (id);

ALTER TABLE ONLY content.avatar_configs
    ADD CONSTRAINT avatar_configs_pkey PRIMARY KEY (lesson_version_id);

ALTER TABLE ONLY content.exercises
    ADD CONSTRAINT exercises_pkey PRIMARY KEY (id);

ALTER TABLE ONLY content.grammar_topics
    ADD CONSTRAINT grammar_topics_pkey PRIMARY KEY (id);

ALTER TABLE ONLY content.learning_objectives
    ADD CONSTRAINT learning_objectives_pkey PRIMARY KEY (id);

ALTER TABLE ONLY content.lesson_versions
    ADD CONSTRAINT lesson_versions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY content.lessons
    ADD CONSTRAINT lessons_pkey PRIMARY KEY (id);

ALTER TABLE ONLY content.lexical_units
    ADD CONSTRAINT lexical_units_pkey PRIMARY KEY (id);

ALTER TABLE ONLY iam.email_tokens
    ADD CONSTRAINT email_tokens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY iam.login_attempts
    ADD CONSTRAINT login_attempts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY iam.organizations
    ADD CONSTRAINT organizations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY iam.permissions
    ADD CONSTRAINT permissions_pkey PRIMARY KEY (code);

ALTER TABLE ONLY iam.role_permissions
    ADD CONSTRAINT role_permissions_pkey PRIMARY KEY (role_code, permission_code);

ALTER TABLE ONLY iam.roles
    ADD CONSTRAINT roles_pkey PRIMARY KEY (code);

ALTER TABLE ONLY iam.sessions
    ADD CONSTRAINT sessions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY iam.student_tokens
    ADD CONSTRAINT student_tokens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY iam.students
    ADD CONSTRAINT students_pkey PRIMARY KEY (id);

ALTER TABLE ONLY iam.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

ALTER TABLE ONLY learning.answer_events
    ADD CONSTRAINT answer_events_pkey PRIMARY KEY (id, occurred_at);

ALTER TABLE ONLY learning.assignments
    ADD CONSTRAINT assignments_pkey PRIMARY KEY (id);

ALTER TABLE ONLY learning.attempt_feedback
    ADD CONSTRAINT attempt_feedback_pkey PRIMARY KEY (attempt_id);

ALTER TABLE ONLY learning.attempts
    ADD CONSTRAINT attempts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY learning.dialog_turns
    ADD CONSTRAINT dialog_turns_pkey PRIMARY KEY (id, occurred_at);

ALTER TABLE ONLY learning.issue_tasks
    ADD CONSTRAINT issue_tasks_pkey PRIMARY KEY (assignment_id, task_key);

ALTER TABLE ONLY learning.issues
    ADD CONSTRAINT issues_id_assignment_uq UNIQUE (id, assignment_id);

ALTER TABLE ONLY learning.issues
    ADD CONSTRAINT issues_pkey PRIMARY KEY (id);

ALTER TABLE ONLY learning.presence
    ADD CONSTRAINT presence_pkey PRIMARY KEY (student_id);

ALTER TABLE ONLY learning.task_progress
    ADD CONSTRAINT task_progress_pkey PRIMARY KEY (attempt_id, task_key);

ALTER TABLE ONLY lexicon.languages
    ADD CONSTRAINT languages_pkey PRIMARY KEY (code);

ALTER TABLE ONLY lexicon.levels
    ADD CONSTRAINT levels_pkey PRIMARY KEY (code);

ALTER TABLE ONLY ops.live_events
    ADD CONSTRAINT live_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY ops.pipeline_calls
    ADD CONSTRAINT pipeline_calls_pkey PRIMARY KEY (id, started_at);

CREATE UNIQUE INDEX classes_join_code_uq ON classroom.classes USING btree (join_code);

CREATE INDEX classes_teacher_idx ON classroom.classes USING btree (org_id, teacher_id) WHERE (status = 'active'::text);

CREATE INDEX enrollments_class_active_idx ON classroom.enrollments USING btree (class_id, last_seen_at DESC) WHERE (status = 'active'::text);

CREATE UNIQUE INDEX enrollments_class_student_uq ON classroom.enrollments USING btree (class_id, student_id);

CREATE INDEX enrollments_student_idx ON classroom.enrollments USING btree (student_id) WHERE (status = 'active'::text);

CREATE UNIQUE INDEX exercises_pos_uq ON content.exercises USING btree (lesson_version_id, "position");

CREATE INDEX exercises_version_idx ON content.exercises USING btree (lesson_version_id);

CREATE UNIQUE INDEX grammar_topics_pos_uq ON content.grammar_topics USING btree (lesson_version_id, "position");

CREATE UNIQUE INDEX learning_objectives_pos_uq ON content.learning_objectives USING btree (lesson_version_id, "position");

CREATE INDEX lesson_versions_lesson_idx ON content.lesson_versions USING btree (lesson_id, version DESC);

CREATE UNIQUE INDEX lesson_versions_no_uq ON content.lesson_versions USING btree (lesson_id, version);

CREATE INDEX lessons_author_idx ON content.lessons USING btree (org_id, author_id, created_at DESC) WHERE (deleted_at IS NULL);

CREATE UNIQUE INDEX lexical_units_pos_uq ON content.lexical_units USING btree (lesson_version_id, "position");

CREATE INDEX lexical_units_version_idx ON content.lexical_units USING btree (lesson_version_id);

CREATE UNIQUE INDEX email_tokens_hash_uq ON iam.email_tokens USING btree (token_hash);

CREATE INDEX email_tokens_user_idx ON iam.email_tokens USING btree (user_id, kind);

CREATE INDEX login_attempts_lookup_idx ON iam.login_attempts USING btree (email_normalized, ip, occurred_at DESC);

CREATE INDEX sessions_expires_idx ON iam.sessions USING btree (expires_at);

CREATE UNIQUE INDEX sessions_token_uq ON iam.sessions USING btree (token_hash);

CREATE INDEX sessions_user_idx ON iam.sessions USING btree (user_id);

CREATE UNIQUE INDEX student_tokens_hash_uq ON iam.student_tokens USING btree (token_hash);

CREATE INDEX student_tokens_student_idx ON iam.student_tokens USING btree (student_id);

CREATE INDEX students_org_idx ON iam.students USING btree (org_id);

CREATE UNIQUE INDEX students_public_code_uq ON iam.students USING btree (public_code);

CREATE UNIQUE INDEX users_email_uq ON iam.users USING btree (email_normalized) WHERE (deleted_at IS NULL);

CREATE INDEX users_org_idx ON iam.users USING btree (org_id) WHERE (deleted_at IS NULL);

CREATE INDEX answer_events_attempt_idx ON ONLY learning.answer_events USING btree (attempt_id, occurred_at);

CREATE INDEX answer_events_org_idx ON ONLY learning.answer_events USING btree (org_id, occurred_at);

CREATE INDEX assignments_class_idx ON learning.assignments USING btree (class_id, state);

CREATE UNIQUE INDEX assignments_open_uq ON learning.assignments USING btree (class_id, lesson_version_id) WHERE (state <> 'closed'::text);

CREATE INDEX assignments_version_idx ON learning.assignments USING btree (lesson_version_id);

CREATE INDEX attempt_feedback_org_idx ON learning.attempt_feedback USING btree (org_id, created_at DESC);

CREATE UNIQUE INDEX attempts_assignment_student_uq ON learning.attempts USING btree (assignment_id, student_id);

CREATE INDEX attempts_student_idx ON learning.attempts USING btree (student_id, last_seen_at DESC);

CREATE INDEX dialog_turns_attempt_idx ON ONLY learning.dialog_turns USING btree (attempt_id, occurred_at);

CREATE INDEX issue_tasks_issue_idx ON learning.issue_tasks USING btree (issue_id);

CREATE INDEX issues_assignment_idx ON learning.issues USING btree (assignment_id, opened_at);

CREATE UNIQUE INDEX issues_home_uq ON learning.issues USING btree (assignment_id) WHERE (kind = 'home'::text);

CREATE INDEX task_progress_issue_idx ON learning.task_progress USING btree (issue_id);

CREATE INDEX live_events_browser_idx ON ops.live_events USING btree (org_id, actor_role, actor_id, created_at DESC);

CREATE INDEX pipeline_calls_org_idx ON ONLY ops.pipeline_calls USING btree (org_id, started_at DESC);

CREATE INDEX pipeline_calls_pipeline_idx ON ONLY ops.pipeline_calls USING btree (pipeline, started_at DESC);

CREATE INDEX pipeline_calls_ref_idx ON ONLY ops.pipeline_calls USING btree (ref_id) WHERE (ref_id IS NOT NULL);

CREATE TRIGGER classes_touch BEFORE UPDATE ON classroom.classes FOR EACH ROW EXECUTE FUNCTION public.touch_updated_at();

CREATE TRIGGER avatar_configs_touch BEFORE UPDATE ON content.avatar_configs FOR EACH ROW EXECUTE FUNCTION public.touch_updated_at();

CREATE TRIGGER exercises_touch BEFORE UPDATE ON content.exercises FOR EACH ROW EXECUTE FUNCTION public.touch_updated_at();

CREATE TRIGGER lesson_versions_immutable BEFORE UPDATE ON content.lesson_versions FOR EACH ROW EXECUTE FUNCTION content.guard_published_version();

CREATE TRIGGER lesson_versions_touch BEFORE UPDATE ON content.lesson_versions FOR EACH ROW EXECUTE FUNCTION public.touch_updated_at();

CREATE TRIGGER lessons_touch BEFORE UPDATE ON content.lessons FOR EACH ROW EXECUTE FUNCTION public.touch_updated_at();

CREATE TRIGGER organizations_touch BEFORE UPDATE ON iam.organizations FOR EACH ROW EXECUTE FUNCTION public.touch_updated_at();

CREATE TRIGGER students_touch BEFORE UPDATE ON iam.students FOR EACH ROW EXECUTE FUNCTION public.touch_updated_at();

CREATE TRIGGER users_touch BEFORE UPDATE ON iam.users FOR EACH ROW EXECUTE FUNCTION public.touch_updated_at();

CREATE TRIGGER assignments_touch BEFORE UPDATE ON learning.assignments FOR EACH ROW EXECUTE FUNCTION public.touch_updated_at();

CREATE TRIGGER attempt_feedback_touch BEFORE UPDATE ON learning.attempt_feedback FOR EACH ROW EXECUTE FUNCTION public.touch_updated_at();

CREATE TRIGGER issues_touch BEFORE UPDATE ON learning.issues FOR EACH ROW EXECUTE FUNCTION public.touch_updated_at();

CREATE TRIGGER live_events_no_update BEFORE DELETE OR UPDATE ON ops.live_events FOR EACH ROW EXECUTE FUNCTION ops.live_events_append_only();

ALTER TABLE ONLY classroom.enrollments
    ADD CONSTRAINT enrollments_class_id_fkey FOREIGN KEY (class_id) REFERENCES classroom.classes(id) ON DELETE CASCADE;

ALTER TABLE ONLY content.avatar_configs
    ADD CONSTRAINT avatar_configs_lesson_version_id_fkey FOREIGN KEY (lesson_version_id) REFERENCES content.lesson_versions(id) ON DELETE CASCADE;

ALTER TABLE ONLY content.exercises
    ADD CONSTRAINT exercises_lesson_version_id_fkey FOREIGN KEY (lesson_version_id) REFERENCES content.lesson_versions(id) ON DELETE CASCADE;

ALTER TABLE ONLY content.grammar_topics
    ADD CONSTRAINT grammar_topics_lesson_version_id_fkey FOREIGN KEY (lesson_version_id) REFERENCES content.lesson_versions(id) ON DELETE CASCADE;

ALTER TABLE ONLY content.learning_objectives
    ADD CONSTRAINT learning_objectives_lesson_version_id_fkey FOREIGN KEY (lesson_version_id) REFERENCES content.lesson_versions(id) ON DELETE CASCADE;

ALTER TABLE ONLY content.lesson_versions
    ADD CONSTRAINT lesson_versions_lesson_id_fkey FOREIGN KEY (lesson_id) REFERENCES content.lessons(id) ON DELETE CASCADE;

ALTER TABLE ONLY content.lexical_units
    ADD CONSTRAINT lexical_units_lesson_version_id_fkey FOREIGN KEY (lesson_version_id) REFERENCES content.lesson_versions(id) ON DELETE CASCADE;

ALTER TABLE ONLY iam.email_tokens
    ADD CONSTRAINT email_tokens_org_id_fkey FOREIGN KEY (org_id) REFERENCES iam.organizations(id) ON DELETE CASCADE;

ALTER TABLE ONLY iam.email_tokens
    ADD CONSTRAINT email_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES iam.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY iam.role_permissions
    ADD CONSTRAINT role_permissions_permission_code_fkey FOREIGN KEY (permission_code) REFERENCES iam.permissions(code) ON DELETE CASCADE;

ALTER TABLE ONLY iam.role_permissions
    ADD CONSTRAINT role_permissions_role_code_fkey FOREIGN KEY (role_code) REFERENCES iam.roles(code) ON DELETE CASCADE;

ALTER TABLE ONLY iam.sessions
    ADD CONSTRAINT sessions_impersonated_by_fkey FOREIGN KEY (impersonated_by) REFERENCES iam.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY iam.sessions
    ADD CONSTRAINT sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES iam.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY iam.student_tokens
    ADD CONSTRAINT student_tokens_student_id_fkey FOREIGN KEY (student_id) REFERENCES iam.students(id) ON DELETE CASCADE;

ALTER TABLE ONLY iam.students
    ADD CONSTRAINT students_org_id_fkey FOREIGN KEY (org_id) REFERENCES iam.organizations(id) ON DELETE RESTRICT;

ALTER TABLE ONLY iam.users
    ADD CONSTRAINT users_approved_by_fkey FOREIGN KEY (approved_by) REFERENCES iam.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY iam.users
    ADD CONSTRAINT users_org_id_fkey FOREIGN KEY (org_id) REFERENCES iam.organizations(id) ON DELETE RESTRICT;

ALTER TABLE ONLY iam.users
    ADD CONSTRAINT users_role_code_fkey FOREIGN KEY (role_code) REFERENCES iam.roles(code);

ALTER TABLE ONLY learning.attempt_feedback
    ADD CONSTRAINT attempt_feedback_attempt_id_fkey FOREIGN KEY (attempt_id) REFERENCES learning.attempts(id) ON DELETE CASCADE;

ALTER TABLE ONLY learning.attempts
    ADD CONSTRAINT attempts_assignment_id_fkey FOREIGN KEY (assignment_id) REFERENCES learning.assignments(id) ON DELETE CASCADE;

ALTER TABLE ONLY learning.issue_tasks
    ADD CONSTRAINT issue_tasks_issue_fk FOREIGN KEY (issue_id, assignment_id) REFERENCES learning.issues(id, assignment_id) ON DELETE CASCADE;

ALTER TABLE ONLY learning.issues
    ADD CONSTRAINT issues_assignment_id_fkey FOREIGN KEY (assignment_id) REFERENCES learning.assignments(id) ON DELETE CASCADE;

ALTER TABLE ONLY learning.task_progress
    ADD CONSTRAINT task_progress_attempt_id_fkey FOREIGN KEY (attempt_id) REFERENCES learning.attempts(id) ON DELETE CASCADE;

ALTER TABLE ONLY learning.task_progress
    ADD CONSTRAINT task_progress_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES learning.issues(id) ON DELETE SET NULL;

-- Справочники: языки, уровни, роли и права.

INSERT INTO iam.permissions VALUES ('classes.own', 'Свои классы');
INSERT INTO iam.permissions VALUES ('lessons.own', 'Свои уроки');
INSERT INTO iam.permissions VALUES ('students.own', 'Ученики своих классов');
INSERT INTO iam.permissions VALUES ('users.view', 'Просмотр аккаунтов');
INSERT INTO iam.permissions VALUES ('users.create', 'Создание аккаунта вручную');
INSERT INTO iam.permissions VALUES ('users.block', 'Блокировка аккаунта');
INSERT INTO iam.permissions VALUES ('users.delete', 'Удаление аккаунта');
INSERT INTO iam.permissions VALUES ('users.impersonate', 'Войти как пользователь');
INSERT INTO iam.permissions VALUES ('orgs.view', 'Просмотр организаций');
INSERT INTO iam.permissions VALUES ('orgs.manage', 'Управление организациями');
INSERT INTO iam.permissions VALUES ('plans.view', 'Просмотр тарифов');
INSERT INTO iam.permissions VALUES ('plans.assign', 'Назначение тарифа');
INSERT INTO iam.permissions VALUES ('moderation.queue', 'Очередь модерации');
INSERT INTO iam.permissions VALUES ('moderation.act', 'Действия в очереди модерации');
INSERT INTO iam.permissions VALUES ('cost.view', 'Себестоимость и расход');
INSERT INTO iam.permissions VALUES ('audit.view_own', 'Свои записи журнала действий');
INSERT INTO iam.permissions VALUES ('audit.view_all', 'Весь журнал действий');

INSERT INTO iam.roles VALUES ('teacher', 'Учитель', 'Ведёт свои классы, собирает и назначает уроки');
INSERT INTO iam.roles VALUES ('superadmin', 'Суперадминистратор', 'Полный доступ, включая удаление и тарифы');
INSERT INTO iam.roles VALUES ('support', 'Саппорт', 'Разбирает обращения; может блокировать, но не удалять');
INSERT INTO iam.roles VALUES ('moderator', 'Модератор контента', 'Обрабатывает очередь модерации');

INSERT INTO iam.role_permissions VALUES ('teacher', 'classes.own');
INSERT INTO iam.role_permissions VALUES ('teacher', 'lessons.own');
INSERT INTO iam.role_permissions VALUES ('teacher', 'students.own');
INSERT INTO iam.role_permissions VALUES ('support', 'users.view');
INSERT INTO iam.role_permissions VALUES ('support', 'users.block');
INSERT INTO iam.role_permissions VALUES ('support', 'users.impersonate');
INSERT INTO iam.role_permissions VALUES ('support', 'orgs.view');
INSERT INTO iam.role_permissions VALUES ('support', 'plans.view');
INSERT INTO iam.role_permissions VALUES ('support', 'audit.view_own');
INSERT INTO iam.role_permissions VALUES ('moderator', 'moderation.queue');
INSERT INTO iam.role_permissions VALUES ('moderator', 'moderation.act');
INSERT INTO iam.role_permissions VALUES ('moderator', 'audit.view_own');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'classes.own');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'lessons.own');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'students.own');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'users.view');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'users.create');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'users.block');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'users.delete');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'users.impersonate');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'orgs.view');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'orgs.manage');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'plans.view');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'plans.assign');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'moderation.queue');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'moderation.act');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'cost.view');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'audit.view_own');
INSERT INTO iam.role_permissions VALUES ('superadmin', 'audit.view_all');

INSERT INTO lexicon.languages VALUES ('russian', 'Русский', 'Русский', true, '2026-10-19 17:19:46.432674+03');
INSERT INTO lexicon.languages VALUES ('english', 'Английский', 'English', true, '2026-10-19 17:21:53.747254+03');

INSERT INTO lexicon.levels VALUES ('A1', 1, 'Начальный');
INSERT INTO lexicon.levels VALUES ('A2', 2, 'Базовый');
INSERT INTO lexicon.levels VALUES ('B1', 3, 'Средний');
INSERT INTO lexicon.levels VALUES ('B2', 4, 'Выше среднего');
INSERT INTO lexicon.levels VALUES ('C1', 5, 'Продвинутый');

-- Секции таблиц событий: прошлая неделя и семь вперёд.
SELECT public.ensure_weekly_partitions('learning', 'answer_events', current_date - 7, 8);
SELECT public.ensure_weekly_partitions('learning', 'dialog_turns',  current_date - 7, 8);
SELECT public.ensure_weekly_partitions('ops', 'pipeline_calls', current_date - 7, 8);
