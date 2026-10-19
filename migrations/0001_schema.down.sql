-- 0001_schema — откат: схема MVP целиком.
DROP SCHEMA learning, content, classroom, iam, ops, lexicon CASCADE;
DROP FUNCTION public.ensure_weekly_partitions(text, text, date, int);
DROP FUNCTION public.touch_updated_at();
