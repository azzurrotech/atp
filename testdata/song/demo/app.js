document.getElementById('go') && document.getElementById('go').addEventListener('click', async () => {
  const res = await fetch('/api/pod/table/demo/notes', {headers:{'Accept':'application/json'}});
  document.getElementById('out').textContent = JSON.stringify(await res.json(), null, 2);
});