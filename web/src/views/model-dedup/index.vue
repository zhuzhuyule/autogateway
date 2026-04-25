<template>
  <div class="model-dedup">
    <div class="header">
      <h2>模型去重建议</h2>
      <el-button type="primary" @click="loadSuggestions" :loading="loading">
        刷新
      </el-button>
    </div>

    <el-card v-if="suggestions.length === 0 && !loading">
      <el-empty description="暂无去重建议">
        所有模型都已正确配置
      </el-empty>
    </el-card>

    <el-card v-else>
      <el-table :data="suggestions" stripe border>
        <el-table-column prop="model_name" label="模型名" width="180" />
        <el-table-column prop="source_groups" label="来源分组" min-width="250">
          <template #default="{ row }">
            <el-tag
              v-for="group in row.source_groups"
              :key="group"
              size="small"
              style="margin-right: 5px"
            >
              {{ group }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="suggested_aggregate_name" label="建议聚合分组" width="200" />
        <el-table-column label="操作" width="180">
          <template #default="{ row, $index }">
            <el-button size="small" type="primary" @click="showCreateDialog(row, $index)">
              创建聚合
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="showDialog" title="创建聚合分组" width="600px">
      <el-form v-if="selectedSuggestion" label-width="140px">
        <el-form-item label="聚合分组名称">
          <el-input v-model="selectedSuggestion.aggregate_name" disabled />
        </el-form-item>
        <el-form-item label="模型名称">
          <el-input v-model="selectedSuggestion.model_name" disabled />
        </el-form-item>
        <el-form-item label="子分组配置">
          <el-table :data="selectedSuggestion.sub_groups" size="small">
            <el-table-column prop="name" label="分组名" />
            <el-table-column prop="weight" label="权重" width="100" />
            <el-table-column prop="redirect" label="重定向">
              <template #default="{ row }">
                {{ row.redirect[selectedSuggestion.model_name] }}
              </template>
            </el-table-column>
          </el-table>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showDialog = false">取消</el-button>
        <el-button type="primary" @click="handleCreate" :loading="creating">
          确认创建
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue';
import { ElMessage, ElMessageBox } from 'element-plus';
import {
  modelDedupApi,
  type DedupSuggestion,
  type AggregateSuggestion,
} from '../api/model-dedup';

const suggestions = ref<DedupSuggestion[]>([]);
const loading = ref(false);
const showDialog = ref(false);
const selectedSuggestion = ref<AggregateSuggestion | null>(null);
const creating = ref(false);

async function loadSuggestions() {
  loading.value = true;
  try {
    const resp = await modelDedupApi.getSuggestions();
    if (resp.success) {
      suggestions.value = resp.data;
    } else {
      ElMessage.error('获取建议失败');
    }
  } catch (e) {
    suggestions.value = [];
  } finally {
    loading.value = false;
  }
}

function showCreateDialog(suggestion: DedupSuggestion, index: number) {
  const subGroups = suggestion.source_groups.map((name) => ({
    name,
    weight: Math.floor(100 / suggestion.source_groups.length),
    redirect: {
      [suggestion.model_name]: suggestion.model_name,
    },
  }));

  selectedSuggestion.value = {
    aggregate_name: suggestion.suggested_aggregate_name,
    model_name: suggestion.model_name,
    sub_groups: subGroups,
  };
  showDialog.value = true;
}

async function handleCreate() {
  if (!selectedSuggestion.value) return;

  creating.value = true;
  try {
    const resp = await modelDedupApi.createAggregate(selectedSuggestion.value);
    if (resp.success) {
      ElMessage.success('创建成功');
      showDialog.value = false;
      loadSuggestions();
    } else {
      ElMessage.error('创建失败: ' + resp.error);
    }
  } catch (e) {
    ElMessage.error('创建失败');
  } finally {
    creating.value = false;
  }
}

onMounted(() => {
  loadSuggestions();
});
</script>

<style scoped>
.model-dedup {
  padding: 20px;
}

.header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.header h2 {
  margin: 0;
}
</style>
