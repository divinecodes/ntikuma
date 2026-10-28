// Appended to the actual cgo preamble by selection_linux_test.go. Exercise its
// asynchronous stream reader without a desktop connection or user selections.
#include <stdio.h>
#include <string.h>

static GMainLoop *loop;
static int completed, got_status, got_length;
static const char *expected;

void ntkSelectionResult(uintptr_t handle, void *data, int length, int status) {
  completed++;
  got_status = status;
  got_length = length;
  if (status == 0 && length && memcmp(expected, data, length) != 0) got_status = 99;
  if (loop) g_main_loop_quit(loop);
}

static gboolean timed_out(gpointer data) {
  got_status = 98;
  g_main_loop_quit(loop);
  return G_SOURCE_REMOVE;
}

static int transfer(const char *text, int length, int limit, int cancel, int status, int count) {
  completed = 0;
  expected = text;
  NtkSelection *read = g_new0(NtkSelection, 1);
  read->handle = 1;
  read->cancel = g_cancellable_new();
  read->bytes = g_byte_array_new();
  read->limit = limit;
  read->stream = g_memory_input_stream_new_from_data(text, length, NULL);
  if (cancel) g_cancellable_cancel(read->cancel);
  loop = g_main_loop_new(NULL, FALSE);
  guint timeout = g_timeout_add(1000, timed_out, NULL);
  ntk_selection_next(read);
  g_main_loop_run(loop);
  if (got_status != 98) g_source_remove(timeout);
  g_main_loop_unref(loop);
  loop = NULL;
  if (completed != 1 || got_status != status || got_length != count) {
    fprintf(stderr, "transfer length=%d limit=%d cancel=%d: callbacks=%d status=%d bytes=%d\n",
            length, limit, cancel, completed, got_status, got_length);
    return 1;
  }
  return 0;
}

int main(void) {
  if (transfer("", 0, 5, 0, 0, 0)) return 1;
  if (transfer("hello", 5, 5, 0, 0, 5)) return 1;
  if (transfer("hello!", 6, 5, 0, 2, 6)) return 1;
  if (transfer("hello", 5, 5, 1, 1, 0)) return 1;
  char *large = g_malloc0(200000);
  memset(large, 'x', 200000);
  int result = transfer(large, 200000, 200000, 0, 0, 200000);
  g_free(large);
  if (result) return result;
  // The early completion path must still return a valid caller-owned cancel.
  completed = 0;
  GCancellable *cancel = ntk_selection_start(2, 5);
  ntk_selection_cancel(cancel);
  ntk_selection_unref(cancel);
  if (completed != 1 || got_status != 3) return 1;
  return 0;
}
